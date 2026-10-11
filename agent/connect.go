package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"vartio/internal/entities"
)

func serve(ctx context.Context, cfg config) error {
	go autoUpdate(ctx, cfg)
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		setLink(func(s *linkState) { s.Dialing = true })
		sctx, cancel := context.WithCancel(ctx)
		var manual atomic.Bool
		go func() {
			select {
			case <-reconnectReq:
				manual.Store(true)
				cancel()
			case <-sctx.Done():
			}
		}()
		connected, err := dialOnce(sctx, cfg)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected || manual.Load() {
			backoff = time.Second
		}
		if manual.Load() {
			err = errors.New("reconnect requested")
		}
		setLink(func(s *linkState) {
			s.Connected, s.Dialing, s.Since, s.Err = false, false, time.Now(), err.Error()
		})
		log.Printf("disconnected: %v; retry in %s", err, backoff)
		if manual.Load() {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reconnectReq:
			backoff = time.Second
			continue
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

// dialOnce reports connected=true once the hello handshake succeeded, so the caller
// resets its backoff after a session that was actually up.
func dialOnce(ctx context.Context, cfg config) (bool, error) {
	ent := loadHubEntry(cfg.URL)
	hdr := http.Header{}
	cfg.setAgentAuth(hdr, ent)
	nonce, err := randomHex(16)
	if err != nil {
		return false, err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, cfg.URL, hdr)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	hello := entities.Hello{
		BiosUUID:   cfg.BiosUUID,
		DeviceID:   cfg.DeviceID,
		Device:     cfg.Device,
		HostName:   hostname(),
		Version:    agentVersion,
		MACs:       listMACs(),
		Caps:       caps(),
		Primitives: primitives(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Runner:     runnerName(),
		Upgrade:    pendingReport(),
		Nonce:      nonce,
	}
	if err := conn.WriteJSON(hello); err != nil {
		return false, err
	}
	var ack entities.HelloOK
	if err := conn.ReadJSON(&ack); err != nil {
		return false, err
	}
	if !ack.OK {
		if ack.Message != "" {
			return false, fmt.Errorf("%w: %s: %s", errHello, ack.Code, ack.Message)
		}
		return false, errHello
	}
	changed, err := acceptHello(&ent, nonce, cfg.deviceKey(), ack)
	if err != nil {
		return false, err
	}
	if changed {
		if err := saveHubEntry(cfg.URL, ent); err != nil {
			log.Printf("save hub credential to %s: %v", hubStatePath(), err)
		}
	}
	log.Printf("connected as %s", cfg.DeviceID)
	confirmUpgrade()
	setLink(func(s *linkState) {
		s.Connected, s.Dialing, s.Since, s.Err, s.Probation = true, false, time.Now(), "", ""
	})
	// ReadJSON does not watch ctx; close the socket so shutdown is immediate and
	// the hub sees the disconnect now rather than at the next poll.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	a := &agent{cfg: cfg, conn: conn, term: newTermHub(), display: newDisplayHub(), tun: newTunnelHub()}
	defer a.term.close("")
	defer a.display.close("")
	defer a.tun.close("")
	return true, a.loop(ctx)
}

var errHello = errors.New("hello rejected")

func caps() []string {
	c := []string{entities.CapMultiTerm}
	if upgradeEnabled() {
		c = append(c, entities.CapUpgrade)
	}
	if !loadTunnelPolicy().off {
		c = append(c, entities.CapTunnelTCP)
	}
	c = append(c, entities.CapFsWhich, entities.CapFsRead, entities.CapNetListeners)
	if spawnEnabled() {
		c = append(c, entities.CapProcSpawn)
	}
	if docker() != nil {
		c = append(c, entities.CapDocker, entities.CapDockerInspect)
		if dockerMode() == dockerRW {
			c = append(c, entities.CapDockerExec)
		}
	}
	return c
}

func primitives() map[string]int {
	out := map[string]int{}
	for _, c := range caps() {
		if v, ok := entities.PrimitiveVersions[c]; ok {
			out[c] = v
		}
	}
	return out
}

type agent struct {
	cfg     config
	conn    *websocket.Conn
	term    *termHub
	display *displayHub
	tun     *tunnelHub
	writeMu sync.Mutex
}

func (a *agent) send(fr entities.Frame) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = a.conn.WriteJSON(fr)
}

func (a *agent) loop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var fr entities.Frame
		if err := a.conn.ReadJSON(&fr); err != nil {
			return err
		}
		switch fr.Stream {
		case "in":
			a.term.write(fr.ID, fr.Data)
			continue
		case entities.StreamTunnel:
			a.tun.write(fr.ID, fr.Data)
			continue
		case entities.StreamTunnelClose:
			a.tun.close(fr.ID)
			continue
		}
		if fr.Method == "" {
			continue
		}
		go a.handle(fr)
	}
}

func (a *agent) handle(fr entities.Frame) {
	res, rpcErr := a.dispatch(fr)
	out := entities.Frame{ID: fr.ID}
	if rpcErr != nil {
		out.Error = rpcErr
	} else if res != nil {
		b, _ := json.Marshal(res)
		out.Result = b
	}
	a.writeMu.Lock()
	err := a.conn.WriteJSON(out)
	a.writeMu.Unlock()
	if err != nil {
		log.Printf("write reply: %v", err)
	}
}

func (a *agent) streamOut(id string, p []byte) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = a.conn.WriteJSON(entities.Frame{ID: id, Stream: "out", Data: p})
}

func (a *agent) streamExit(id string) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = a.conn.WriteJSON(entities.Frame{ID: id, Stream: entities.StreamTermExit})
}

func (a *agent) streamDisplay(id string, p []byte) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = a.conn.WriteJSON(entities.Frame{ID: id, Stream: "display", Data: p})
}

func (a *agent) streamUpgrade(id string, st entities.UpgradeStep) {
	b, _ := json.Marshal(st)
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = a.conn.WriteJSON(entities.Frame{ID: id, Stream: entities.StreamUpgrade, Data: b})
}

func hostname() string {
	if h := strings.TrimSpace(getenv("VARTIO_AGENT_HOSTNAME", "GUEST_HOSTNAME")); h != "" {
		return h
	}
	if inContainer() && hostRoot() != "" {
		if b, err := os.ReadFile(hostPath("/etc/hostname")); err == nil {
			if h := strings.TrimSpace(string(b)); h != "" {
				return h
			}
		}
	}
	h, _ := os.Hostname()
	return h
}
