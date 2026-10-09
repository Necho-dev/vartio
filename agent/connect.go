package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"govmomi-wmks/internal/guest"
)

func serve(ctx context.Context, cfg config) error {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := dialOnce(ctx, cfg)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("disconnected: %v; retry in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func dialOnce(ctx context.Context, cfg config) error {
	hdr := http.Header{}
	if cfg.Token != "" {
		hdr.Set("X-Guest-Token", cfg.Token)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, cfg.URL, hdr)
	if err != nil {
		return err
	}
	defer conn.Close()

	hello := guest.Hello{
		BiosUUID: cfg.BiosUUID,
		HostName: hostname(),
		Version:  agentVersion,
		MACs:     listMACs(),
	}
	if err := conn.WriteJSON(hello); err != nil {
		return err
	}
	var ack guest.HelloOK
	if err := conn.ReadJSON(&ack); err != nil {
		return err
	}
	if !ack.OK {
		return errHello
	}
	log.Printf("connected as %s", cfg.BiosUUID)
	a := &agent{cfg: cfg, conn: conn, term: newTermHub(), display: newDisplayHub()}
	return a.loop(ctx)
}

var errHello = errors.New("hello rejected")

type agent struct {
	cfg     config
	conn    *websocket.Conn
	term    *termHub
	display *displayHub
	writeMu sync.Mutex
}

func (a *agent) loop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var fr guest.Frame
		if err := a.conn.ReadJSON(&fr); err != nil {
			return err
		}
		if fr.Stream == "in" {
			a.term.write(fr.Data)
			continue
		}
		if fr.Method == "" {
			continue
		}
		go a.handle(fr)
	}
}

func (a *agent) handle(fr guest.Frame) {
	res, rpcErr := a.dispatch(fr)
	out := guest.Frame{ID: fr.ID}
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
	_ = a.conn.WriteJSON(guest.Frame{ID: id, Stream: "out", Data: p})
}

func (a *agent) streamDisplay(id string, p []byte) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = a.conn.WriteJSON(guest.Frame{ID: id, Stream: "display", Data: p})
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
