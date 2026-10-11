package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"vartio/internal/common"
	"vartio/internal/entities"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}

func writeAPIErr(w http.ResponseWriter, err error) {
	var api *common.Error
	if !errors.As(err, &api) {
		api = &common.Error{Status: http.StatusBadGateway, Code: "error", Message: err.Error()}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(api.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":     api.Code,
		"message":  api.Message,
		"vimFault": nil,
	})
}

func confirmed(r *http.Request, bodyConfirm bool) bool {
	if bodyConfirm {
		return true
	}
	b, _ := strconv.ParseBool(strings.TrimSpace(r.Header.Get("X-Confirm")))
	return b
}

// HandleConnect upgrades an outbound agent WebSocket. Agents dial this; we never dial them.
func (h *Hub) HandleConnect(w http.ResponseWriter, r *http.Request) {
	auth := h.agentAuthorized(r)
	if !auth.ok() {
		writeAPIErr(w, &common.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "invalid or missing guest token"})
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("guest connect upgrade: %v", err)
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var hello entities.Hello
	if err := conn.ReadJSON(&hello); err != nil {
		_ = conn.WriteJSON(entities.Frame{Error: &entities.FrameError{Code: "bad_request", Message: "hello required"}})
		_ = conn.Close()
		return
	}
	hello.BiosUUID = normUUID(hello.BiosUUID)
	hello.DeviceID = normUUID(hello.DeviceID)
	if hello.BiosUUID == "" {
		_ = conn.WriteJSON(entities.Frame{Error: &entities.FrameError{Code: "bad_request", Message: "biosUuid required"}})
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	remote := clientIP(r)
	var ack entities.HelloOK
	err = h.admit(hello)
	if err == nil {
		ack, err = h.helloAuth(hello, auth)
	}
	if err != nil {
		var rpc *RPCError
		errors.As(err, &rpc)
		_ = conn.WriteJSON(entities.HelloOK{OK: false, Code: rpc.Code, Message: rpc.Message})
		_ = conn.Close()
		log.Printf("guest rejected device=%s bios=%s from=%s: %s", hello.Key(), hello.BiosUUID, remote, rpc.Message)
		if h.OnReject != nil {
			h.OnReject(hello, remote, rpc.Code, rpc.Message)
		}
		return
	}
	if err := conn.WriteJSON(ack); err != nil {
		_ = conn.Close()
		return
	}
	h.accept(conn, hello, remote)
	log.Printf("guest connected device=%s bios=%s host=%s ver=%s from=%s", hello.Key(), hello.BiosUUID, hello.HostName, hello.Version, remote)
}

// clientIP is the peer address without port; a reverse proxy's X-Forwarded-For wins.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// DevHandler is the local /server mux: sessions keyed by device id (or biosUuid), no ESXi.
func (h *Hub) DevHandler() http.Handler {
	mux := http.NewServeMux()
	h.RegisterDev(mux)
	return mux
}

// RegisterDev mounts the standalone probe API (connect, sessions, streams) on mux.
func (h *Hub) RegisterDev(mux *http.ServeMux) {
	mux.HandleFunc("GET "+entities.PathConnect, h.HandleConnect)
	mux.HandleFunc("GET "+entities.PathAgentLatest, h.handleAgentLatest)
	mux.HandleFunc("GET "+entities.PathAgentDownload+"/{version}/{os}/{arch}", h.handleAgentDownload)
	mux.HandleFunc("GET "+entities.PathReleases, h.wrap(h.handleReleases))
	mux.HandleFunc("POST "+entities.PathUpgrade, h.wrap(h.handleUpgradeBatch))
	mux.HandleFunc("GET "+entities.PathUpgrade, h.wrap(h.handleUpgrades))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/upgrade", h.wrap(h.handleUpgrade))
	mux.HandleFunc("GET "+entities.PathHubKey, h.wrap(h.handleHubKey))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/credential/reset", h.wrap(h.handleCredentialReset))
	mux.HandleFunc("GET "+entities.PathSessions, h.wrap(h.handleSessions))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}", h.wrap(h.handleSessionInfo))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/health", h.wrap(h.handleHealth))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/procs", h.wrap(h.handleProcsGet))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/procs", h.wrap(h.handleProcsPost))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/services", h.wrap(h.handleServicesGet))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/services", h.wrap(h.handleServicesPost))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/exec", h.wrap(h.handleExec))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/term", h.wrap(h.handleTerm))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/docker/inspect", h.wrap(h.handleDockerInspect))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/docker/logs", h.wrap(h.handleDockerLogs))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/docker/compose", h.wrap(h.handleDockerCompose))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/power", h.wrap(h.handlePower))
	mux.HandleFunc("GET "+entities.PathTermWS, h.HandleTermWS)
	mux.HandleFunc("GET "+entities.PathDisplayWS, h.HandleDisplayWS)
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/display", h.wrap(h.handleDisplayGet))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/display", h.wrap(h.handleDisplayPost))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/files", h.wrap(h.handleFilesList))
	mux.HandleFunc("PUT "+entities.PathSessions+"/{id}/files", h.wrap(h.handleFilesPut))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/files/content", h.wrap(h.handleFilesContent))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/files/op", h.wrap(h.handleFilesOp))
	mux.HandleFunc("GET "+entities.PathSessions+"/{id}/trash", h.wrap(h.handleTrashList))
	mux.HandleFunc("POST "+entities.PathSessions+"/{id}/trash", h.wrap(h.handleTrashOp))
	h.RegisterTunnels(mux, entities.PathSessions)
}

func (h *Hub) wrap(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			writeAPIErr(w, err)
		}
	}
}

func (h *Hub) handleSessions(w http.ResponseWriter, r *http.Request) error {
	return writeJSON(w, h.List())
}

func (h *Hub) handleSessionInfo(w http.ResponseWriter, r *http.Request) error {
	info, ok := h.Info(r.PathValue("id"))
	if !ok {
		return common.ErrOffline()
	}
	return writeJSON(w, info)
}

func (h *Hub) handleHealth(w http.ResponseWriter, r *http.Request) error {
	out, err := h.Health(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleDockerInspect(w http.ResponseWriter, r *http.Request) error {
	out, err := h.DockerInspect(r.Context(), r.PathValue("id"), r.URL.Query().Get("container"))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(out)
	return err
}

func (h *Hub) handleDockerLogs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	tail, _ := strconv.Atoi(q.Get("tail"))
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	until, _ := strconv.ParseInt(q.Get("until"), 10, 64)
	out, err := h.DockerLogs(r.Context(), r.PathValue("id"), q.Get("container"), tail, since, until)
	if err != nil {
		return err
	}
	h.audit(r.PathValue("id"), "docker.logs", "容器 "+q.Get("container"), clientIP(r), nil)
	return writeJSON(w, out)
}

func (h *Hub) handleDockerCompose(w http.ResponseWriter, r *http.Request) error {
	project := r.URL.Query().Get("project")
	out, err := h.DockerCompose(r.Context(), r.PathValue("id"), project)
	if err != nil {
		return err
	}
	h.audit(r.PathValue("id"), "docker.compose", "项目 "+project, clientIP(r), nil)
	return writeJSON(w, out)
}

func (h *Hub) handleProcsGet(w http.ResponseWriter, r *http.Request) error {
	out, err := h.Procs(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleProcsPost(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name    string `json:"name"`
		Action  string `json:"action"`
		Confirm bool   `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	out, err := h.ProcAction(r.Context(), r.PathValue("id"), body.Name, body.Action, confirmed(r, body.Confirm))
	h.record(r, "proc."+body.Action, body.Name, err)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleServicesGet(w http.ResponseWriter, r *http.Request) error {
	out, err := h.Services(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleServicesPost(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name    string `json:"name"`
		Action  string `json:"action"`
		Confirm bool   `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	out, err := h.ServiceAction(r.Context(), r.PathValue("id"), body.Name, body.Action, confirmed(r, body.Confirm))
	h.record(r, "service."+body.Action, body.Name, err)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleExec(w http.ResponseWriter, r *http.Request) error {
	var req entities.ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		return common.ErrBad("invalid json")
	}
	out, err := h.Exec(r.Context(), r.PathValue("id"), req)
	detail := execLine(req)
	if err == nil {
		detail += " → exit " + strconv.Itoa(out.ExitCode)
		if out.TimedOut {
			detail += "（超时）"
		}
	}
	h.record(r, "exec", detail, err)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleTerm(w http.ResponseWriter, r *http.Request) error {
	var target entities.TermOpenRequest
	if err := json.NewDecoder(r.Body).Decode(&target); err != nil && !errors.Is(err, io.EOF) {
		return common.ErrBad("invalid json")
	}
	out, err := h.IssueTerm(r.PathValue("id"), target)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handlePower(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Action  string `json:"action"`
		Confirm bool   `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	out, err := h.Power(r.Context(), r.PathValue("id"), body.Action, confirmed(r, body.Confirm))
	h.record(r, "power."+body.Action, "", err)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleDisplayGet(w http.ResponseWriter, _ *http.Request) error {
	return writeJSON(w, entities.DisplayInfo{
		ViewOnly: true,
		FPS:      entities.DisplayFPS,
		MaxSide:  entities.DisplayMaxSide,
		Note:     "POST for a one-shot " + entities.PathDisplayWS + " ticket. JPEG up to 10fps, max side 2560, quality=auto|smooth|hd; no input. Does not fall back to hypervisor screenshot.",
	})
}

func (h *Hub) handleDisplayPost(w http.ResponseWriter, r *http.Request) error {
	out, err := h.IssueDisplay(r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleFilesList(w http.ResponseWriter, r *http.Request) error {
	out, err := h.FilesList(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleFilesContent(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	path := r.URL.Query().Get("path")
	err := h.WriteFile(w, ctx, r.PathValue("id"), path)
	if r.URL.Query().Get("preview") == "" {
		h.record(r, "file.download", path, err)
	}
	return err
}

func (h *Hub) handleFilesOp(w http.ResponseWriter, r *http.Request) error {
	var req entities.FilesOpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return common.ErrBad("invalid json")
	}
	req.Confirm = confirmed(r, req.Confirm)
	out, err := h.FilesOp(r.Context(), r.PathValue("id"), req)
	if req.Op != entities.FileOpZip {
		detail := req.Path
		if req.Op == entities.FileOpRename {
			detail += " → " + req.To
		}
		h.record(r, "file."+req.Op, detail, err)
	}
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleTrashList(w http.ResponseWriter, r *http.Request) error {
	out, err := h.Trash(r.Context(), r.PathValue("id"), entities.TrashRequest{Op: entities.TrashList})
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleTrashOp(w http.ResponseWriter, r *http.Request) error {
	var req entities.TrashRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return common.ErrBad("invalid json")
	}
	req.Confirm = confirmed(r, req.Confirm)
	out, err := h.Trash(r.Context(), r.PathValue("id"), req)
	detail := ""
	switch {
	case len(out.Paths) > 0:
		detail = strings.Join(out.Paths, ", ")
	case req.Op == entities.TrashPurge:
		detail = strconv.Itoa(len(req.IDs)) + " 项"
	case req.Op == entities.TrashEmpty && err == nil:
		detail = strconv.Itoa(out.Done) + " 项"
	}
	h.record(r, "trash."+req.Op, detail, err)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleFilesPut(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Query().Get("path")
	if path == "" {
		return common.ErrBad("path required")
	}
	qConfirm, _ := strconv.ParseBool(r.URL.Query().Get("confirm"))
	sha := strings.TrimSpace(r.Header.Get("X-Content-SHA256"))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	out, err := h.ReadFile(ctx, r.PathValue("id"), path, r.Body, r.ContentLength, sha, confirmed(r, qConfirm))
	detail := path
	if err == nil {
		detail += " (" + sizeNote(out.Size) + ")"
	}
	h.record(r, "file.upload", detail, err)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) HandleTermWS(w http.ResponseWriter, r *http.Request) {
	t, ok := h.takeTicket(r.URL.Query().Get("id"), "term")
	if !ok {
		http.Error(w, "ticket not found or already used", http.StatusGone)
		return
	}
	s := h.get(t.Key)
	if s == nil {
		http.Error(w, "guest agent not connected", http.StatusConflict)
		return
	}
	browser, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()

	ctx := r.Context()
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols <= 0 || rows <= 0 {
		cols, rows = 120, 32
	}
	sink, err := s.attachTerm(t.ID)
	if err != nil {
		_ = browser.WriteMessage(websocket.TextMessage, []byte("\r\n\x1b[31m"+err.Error()+"\x1b[0m\r\n"))
		return
	}
	open := t.Term
	open.ID, open.Cols, open.Rows = t.ID, cols, rows
	kind, detail := "term", ""
	if open.Mode != "" {
		kind, detail = "docker."+open.Mode, "容器 "+open.Container
	}
	if _, err := s.call(ctx, t.ID, entities.MethodTermOpen, open); err != nil {
		s.detachTerm(sink)
		if open.Mode != "" {
			h.audit(t.Key, kind, detail, clientIP(r), err)
		}
		_ = browser.WriteMessage(websocket.TextMessage, []byte("\r\n\x1b[31m"+err.Error()+"\x1b[0m\r\n"))
		return
	}
	opened := time.Now()
	switch open.Mode {
	case "":
		h.audit(t.Key, "term.open", "", clientIP(r), nil)
	default:
		h.audit(t.Key, kind, detail, clientIP(r), nil)
	}
	if open.Mode != entities.TermLogs {
		defer func() {
			h.audit(t.Key, "term.close", strings.TrimSpace(detail+" 时长 "+time.Since(opened).Round(time.Second).String()), clientIP(r), nil)
		}()
	}
	defer func() {
		// Unregistered means the PTY is gone or now owned by a newer terminal.
		if !s.detachTerm(sink) {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), entities.TermOpenTimeout)
		_, _ = s.call(c, t.ID+"-close", entities.MethodTermClose, map[string]string{"id": t.ID})
		cancel()
	}()

	idle := time.NewTimer(entities.TermIdle)
	defer idle.Stop()
	activity := make(chan struct{}, 1)
	errc := make(chan error, 2)
	go func() {
		for {
			typ, data, err := browser.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			select {
			case activity <- struct{}{}:
			default:
			}
			if typ == websocket.TextMessage {
				if size, ok := parseResize(data); ok {
					size.ID = t.ID
					c, cancel := context.WithTimeout(ctx, entities.TermOpenTimeout)
					_, _ = s.call(c, t.ID+"-resize-"+strconv.FormatInt(time.Now().UnixNano(), 36), entities.MethodTermResize, size)
					cancel()
					continue
				}
			}
			_ = s.write(entities.Frame{ID: t.ID, Stream: "in", Data: data})
		}
	}()
	go func() {
		for {
			select {
			case <-s.closed:
				errc <- errOffline
				return
			case <-idle.C:
				_ = browser.WriteMessage(websocket.BinaryMessage, []byte("\r\n[idle timeout]\r\n"))
				errc <- context.DeadlineExceeded
				return
			case <-activity:
				idle.Reset(entities.TermIdle)
			case <-sink.done:
				if sink.bye != "" {
					_ = browser.WriteMessage(websocket.BinaryMessage, []byte("\r\n\x1b[90m["+sink.bye+"]\x1b[0m\r\n"))
				}
				errc <- errOffline
				return
			case b := <-sink.out:
				idle.Reset(entities.TermIdle)
				if err := browser.WriteMessage(websocket.BinaryMessage, b); err != nil {
					errc <- err
					return
				}
			}
		}
	}()
	<-errc
}

// parseResize recognises the browser control message {"type":"resize","cols":N,"rows":N}.
// Anything else on the socket is keyboard input.
func parseResize(data []byte) (entities.TermOpenRequest, bool) {
	if len(data) < 8 || data[0] != '{' {
		return entities.TermOpenRequest{}, false
	}
	var m struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if json.Unmarshal(data, &m) != nil || m.Type != "resize" || m.Cols <= 0 || m.Rows <= 0 {
		return entities.TermOpenRequest{}, false
	}
	return entities.TermOpenRequest{Cols: m.Cols, Rows: m.Rows}, true
}

// displayMsg is a JSON text frame on the display WebSocket. Hub → browser:
// "info" (screens, current screen, quality), "screen" / "quality" (switched),
// "error". Browser → hub: "screen" or "quality" to switch.
type displayMsg struct {
	Type    string            `json:"type"`
	Screens []entities.Screen `json:"screens,omitempty"`
	Screen  int               `json:"screen"`
	Quality string            `json:"quality,omitempty"`
	Message string            `json:"message,omitempty"`
}

func (h *Hub) HandleDisplayWS(w http.ResponseWriter, r *http.Request) {
	t, ok := h.takeTicket(r.URL.Query().Get("id"), "display")
	if !ok {
		http.Error(w, "ticket not found or already used", http.StatusGone)
		return
	}
	s := h.get(t.Key)
	if s == nil {
		http.Error(w, "guest agent not connected", http.StatusConflict)
		return
	}
	browser, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()

	// Register the sink before open so the first frame, sent during the RPC,
	// lands on this page and not on another one.
	sink, err := s.attachDisplay(t.ID)
	if err != nil {
		_ = browser.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		return
	}
	defer s.detachDisplay(sink)

	ctx := r.Context()
	raw, err := s.call(ctx, t.ID, entities.MethodDisplayOpen, entities.DisplayOpenRequest{Quality: r.URL.Query().Get("quality")})
	if err != nil {
		_ = browser.WriteMessage(websocket.TextMessage, []byte("display.open: "+err.Error()))
		return
	}
	// Text frames to the browser are JSON control messages; binary frames are JPEGs.
	var wmu sync.Mutex
	write := func(kind int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return browser.WriteMessage(kind, b)
	}
	var res entities.DisplayOpenResult
	_ = json.Unmarshal(raw, &res)
	if len(res.Screens) > 0 || res.Quality != "" {
		b, _ := json.Marshal(displayMsg{Type: "info", Screens: res.Screens, Screen: res.Screen, Quality: res.Quality})
		_ = write(websocket.TextMessage, b)
	}
	opened := time.Now()
	h.audit(t.Key, "display.open", "", clientIP(r), nil)
	defer func() {
		h.audit(t.Key, "display.close", "时长 "+time.Since(opened).Round(time.Second).String(), clientIP(r), nil)
	}()
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), entities.TermOpenTimeout)
		_, _ = s.call(c, t.ID+"-close", entities.MethodDisplayClose, map[string]string{"id": t.ID})
		cancel()
	}()

	idle := time.NewTimer(entities.DisplayIdle)
	defer idle.Stop()
	errc := make(chan error, 2)
	go func() {
		n := 0
		for {
			kind, data, err := browser.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			// View-only: a browser may only pick another monitor or quality profile.
			var m displayMsg
			if kind != websocket.TextMessage || json.Unmarshal(data, &m) != nil {
				continue
			}
			var method, what string
			var params entities.DisplayOpenRequest
			switch {
			case m.Type == "screen" && len(res.Screens) > 0:
				method, what, params = entities.MethodDisplaySelect, "切换屏幕", entities.DisplayOpenRequest{ID: t.ID, Screen: m.Screen}
			case m.Type == "quality" && res.Quality != "":
				method, what, params = entities.MethodDisplayQuality, "切换清晰度", entities.DisplayOpenRequest{ID: t.ID, Quality: m.Quality}
			default:
				continue
			}
			n++
			c, cancel := context.WithTimeout(ctx, entities.TermOpenTimeout)
			out, err := s.call(c, fmt.Sprintf("%s-ctl-%d", t.ID, n), method, params)
			cancel()
			reply := displayMsg{Type: m.Type, Screen: m.Screen, Quality: m.Quality}
			if err != nil {
				reply = displayMsg{Type: "error", Message: what + "失败：" + err.Error()}
			} else if m.Type == "quality" {
				var q struct {
					Quality string `json:"quality"`
				}
				if json.Unmarshal(out, &q) == nil && q.Quality != "" {
					reply.Quality = q.Quality
				}
			}
			b, _ := json.Marshal(reply)
			_ = write(websocket.TextMessage, b)
		}
	}()
	go func() {
		for {
			select {
			case <-s.closed:
				errc <- errOffline
				return
			case <-sink.done:
				errc <- errOffline
				return
			case <-idle.C:
				errc <- context.DeadlineExceeded
				return
			case b, ok := <-sink.out:
				if !ok {
					errc <- errOffline
					return
				}
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(entities.DisplayIdle)
				if err := write(websocket.BinaryMessage, b); err != nil {
					errc <- err
					return
				}
			}
		}
	}()
	<-errc
}
