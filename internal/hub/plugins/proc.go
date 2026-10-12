package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/Necho-dev/vartio/sdk"
	"vartio/internal/common"
)

type proc struct {
	manifest Manifest
	ui       bool
	proxy    *httputil.ReverseProxy
	cmd      *exec.Cmd
	host     *http.Server
	token    string
}

type helloLine struct {
	Listen   string   `json:"listen"`
	UI       bool     `json:"ui"`
	Manifest Manifest `json:"manifest"`
}

func (r *Registry) ready(w http.ResponseWriter, name string) *proc {
	r.mu.Lock()
	p := r.running[name]
	starting := r.starting[name]
	problem := r.problems[name]
	wanted := contains(r.wanted, name)
	_, known := r.catalogBy(name)
	r.mu.Unlock()
	if p != nil {
		return p
	}
	if !known && !wanted && !starting {
		WriteError(w, errNotFound(name))
		return nil
	}
	if !wanted && !starting {
		WriteError(w, &common.Error{Status: http.StatusNotFound, Code: "plugin_not_installed", Message: "plugin " + name + " is not installed"})
		return nil
	}
	msg := "plugin " + name + " is not installed"
	code := "plugin_not_installed"
	status := http.StatusNotFound
	if starting {
		msg = "plugin " + name + " is still installing"
		code = "plugin_installing"
		status = http.StatusServiceUnavailable
	} else if problem != "" {
		msg = problem
		code = "plugin_unavailable"
		status = http.StatusBadGateway
	}
	WriteError(w, &common.Error{Status: status, Code: code, Message: msg})
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func proxyTo(raw string) (*httputil.ReverseProxy, error) {
	target, err := url.Parse("http://" + raw)
	if err != nil {
		return nil, err
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			WriteError(w, &common.Error{Status: http.StatusBadGateway, Code: "plugin_unavailable", Message: err.Error()})
		},
	}, nil
}

func (r *Registry) start(ctx context.Context, item CatalogItem, bin string) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	hostLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"VARTIO_PLUGIN_HUB=http://"+hostLn.Addr().String(),
		"VARTIO_PLUGIN_TOKEN="+token,
		"VARTIO_PLUGIN_MANIFEST="+filepath.Join(filepath.Dir(bin), sdk.ManifestName),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		hostLn.Close()
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		hostLn.Close()
		return err
	}
	hello, err := readHello(ctx, stdout)
	if err != nil {
		_ = cmd.Process.Kill()
		hostLn.Close()
		return err
	}
	if !sdk.SameManifest(hello.Manifest, item.Manifest) || hello.UI != item.UI {
		_ = cmd.Process.Kill()
		hostLn.Close()
		return &common.Error{Status: http.StatusBadGateway, Code: "plugin_manifest", Message: "plugin " + item.Name + " announced a different manifest"}
	}
	px, err := proxyTo(hello.Listen)
	if err != nil {
		_ = cmd.Process.Kill()
		hostLn.Close()
		return err
	}
	p := &proc{manifest: hello.Manifest, ui: hello.UI, proxy: px, cmd: cmd, token: token}
	api := r.hostAPI(p)
	p.host = &http.Server{Handler: api, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.host.Serve(hostLn) }()
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
		r.mu.Lock()
		if r.running[item.Name] == p {
			delete(r.running, item.Name)
			r.problems[item.Name] = "plugin process exited"
		}
		r.mu.Unlock()
		_ = p.host.Close()
	}()
	r.mu.Lock()
	if old := r.running[item.Name]; old != nil {
		r.mu.Unlock()
		old.stop()
		r.mu.Lock()
	}
	r.running[item.Name] = p
	r.starting[item.Name] = false
	delete(r.problems, item.Name)
	r.mu.Unlock()
	log.Printf("plugin %s ready %s", item.Name, hello.Listen)
	return nil
}

func readHello(ctx context.Context, r io.Reader) (helloLine, error) {
	ch := make(chan helloLine, 1)
	errc := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(r)
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				errc <- err
				return
			}
			errc <- io.ErrUnexpectedEOF
			return
		}
		var h helloLine
		if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
			errc <- err
			return
		}
		if h.Listen == "" {
			errc <- errors.New("plugin announced no listen address")
			return
		}
		ch <- h
	}()
	select {
	case <-ctx.Done():
		return helloLine{}, ctx.Err()
	case err := <-errc:
		return helloLine{}, err
	case h := <-ch:
		return h, nil
	case <-time.After(30 * time.Second):
		return helloLine{}, errors.New("plugin did not announce its listen address")
	}
}

func (p *proc) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	if p.host != nil {
		_ = p.host.Close()
	}
}

func (r *Registry) hostAPI(p *proc) http.Handler {
	mux := http.NewServeMux()
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("Authorization") != "Bearer "+p.token {
				WriteError(w, &common.Error{Status: http.StatusUnauthorized, Code: "plugin_forbidden", Message: "plugin token rejected"})
				return
			}
			next(w, req)
		}
	}
	h := &scopedHost{hub: r.hub, m: p.manifest, values: func(agent string) map[string]string {
		return r.configValues(p.manifest.Name, agent)
	}}
	mux.HandleFunc("GET /agents", auth(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, h.Agents())
	}))
	mux.HandleFunc("GET /agents/{id}", auth(func(w http.ResponseWriter, req *http.Request) {
		info, ok := h.Agent(req.PathValue("id"))
		if !ok || !info.Online {
			WriteError(w, common.ErrOffline())
			return
		}
		if !sdk.MachineMatch(h.m.Machines, info.OS, info.Runner, info.Caps) {
			WriteError(w, errForbidden(h.m.Name+" is not for this machine"))
			return
		}
		writeJSON(w, http.StatusOK, info)
	}))
	mux.HandleFunc("POST /dial", auth(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Agent  string `json:"agent"`
			Target string `json:"target"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			WriteError(w, common.ErrBad("invalid json"))
			return
		}
		conn, err := h.DialTCP(req.Context(), body.Agent, body.Target)
		if err != nil {
			WriteError(w, err)
			return
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			conn.Close()
			WriteError(w, err)
			return
		}
		go bridgeOne(ln, conn)
		writeJSON(w, http.StatusOK, map[string]string{"addr": ln.Addr().String()})
	}))
	mux.HandleFunc("POST /call", auth(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Agent     string          `json:"agent"`
			Primitive string          `json:"primitive"`
			Params    json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			WriteError(w, common.ErrBad("invalid json"))
			return
		}
		raw, err := h.Call(req.Context(), body.Agent, body.Primitive, body.Params)
		if err != nil {
			WriteError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	mux.HandleFunc("GET /config", auth(func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, h.values(req.URL.Query().Get("agent")))
	}))
	mux.HandleFunc("POST /audit", auth(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Agent  string `json:"agent"`
			Kind   string `json:"kind"`
			Detail string `json:"detail"`
			Error  string `json:"error"`
			Remote string `json:"remote"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			WriteError(w, common.ErrBad("invalid json"))
			return
		}
		var callErr error
		if body.Error != "" {
			callErr = errors.New(body.Error)
		}
		fake := req.Clone(req.Context())
		if body.Remote != "" {
			fake.Header.Set("X-Forwarded-For", body.Remote)
		}
		h.Audit(fake, body.Agent, body.Kind, body.Detail, callErr)
		w.WriteHeader(http.StatusNoContent)
	}))
	return mux
}

func bridgeOne(ln net.Listener, agent net.Conn) {
	defer ln.Close()
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(15 * time.Second))
	}
	local, err := ln.Accept()
	if err != nil {
		agent.Close()
		return
	}
	defer local.Close()
	defer agent.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(agent, local); _ = agent.Close() }()
	go func() { defer wg.Done(); _, _ = io.Copy(local, agent); _ = local.Close() }()
	wg.Wait()
}
