package guest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"govmomi-wmks/internal/esxi"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(v)
}

func writeAPIErr(w http.ResponseWriter, err error) {
	var api *esxi.Error
	if !errors.As(err, &api) {
		api = &esxi.Error{Status: http.StatusBadGateway, Code: "error", Message: err.Error()}
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
	if !tokenOK(h.Token, guestToken(r)) {
		writeAPIErr(w, &esxi.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "invalid or missing guest token"})
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("guest connect upgrade: %v", err)
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var hello Hello
	if err := conn.ReadJSON(&hello); err != nil {
		_ = conn.WriteJSON(Frame{Error: &FrameError{Code: "bad_request", Message: "hello required"}})
		_ = conn.Close()
		return
	}
	hello.BiosUUID = normUUID(hello.BiosUUID)
	if hello.BiosUUID == "" {
		_ = conn.WriteJSON(Frame{Error: &FrameError{Code: "bad_request", Message: "biosUuid required"}})
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if err := conn.WriteJSON(HelloOK{OK: true}); err != nil {
		_ = conn.Close()
		return
	}
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	h.accept(conn, hello, remote)
	log.Printf("guest connected bios=%s host=%s ver=%s from=%s", hello.BiosUUID, hello.HostName, hello.Version, remote)
}

// DevHandler is the local /server mux: sessions keyed by biosUuid, no ESXi.
func (h *Hub) DevHandler() http.Handler {
	mux := http.NewServeMux()
	h.RegisterDev(mux)
	return mux
}

// RegisterDev mounts the standalone probe API (connect, sessions, streams) on mux.
func (h *Hub) RegisterDev(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PathConnect, h.HandleConnect)
	mux.HandleFunc("GET "+PathSessions, h.wrap(h.handleSessions))
	mux.HandleFunc("GET "+PathSessions+"/{bios}", h.wrap(h.handleSessionInfo))
	mux.HandleFunc("GET "+PathSessions+"/{bios}/health", h.wrap(h.handleHealth))
	mux.HandleFunc("GET "+PathSessions+"/{bios}/procs", h.wrap(h.handleProcsGet))
	mux.HandleFunc("POST "+PathSessions+"/{bios}/procs", h.wrap(h.handleProcsPost))
	mux.HandleFunc("GET "+PathSessions+"/{bios}/services", h.wrap(h.handleServicesGet))
	mux.HandleFunc("POST "+PathSessions+"/{bios}/services", h.wrap(h.handleServicesPost))
	mux.HandleFunc("POST "+PathSessions+"/{bios}/exec", h.wrap(h.handleExec))
	mux.HandleFunc("POST "+PathSessions+"/{bios}/term", h.wrap(h.handleTerm))
	mux.HandleFunc("POST "+PathSessions+"/{bios}/power", h.wrap(h.handlePower))
	mux.HandleFunc("GET "+PathTermWS, h.HandleTermWS)
	mux.HandleFunc("GET "+PathDisplayWS, h.HandleDisplayWS)
	mux.HandleFunc("GET "+PathSessions+"/{bios}/display", h.wrap(h.handleDisplayGet))
	mux.HandleFunc("POST "+PathSessions+"/{bios}/display", h.wrap(h.handleDisplayPost))
	mux.HandleFunc("GET "+PathSessions+"/{bios}/files", h.wrap(h.handleFilesList))
	mux.HandleFunc("PUT "+PathSessions+"/{bios}/files", h.wrap(h.handleFilesPut))
	mux.HandleFunc("GET "+PathSessions+"/{bios}/files/content", h.wrap(h.handleFilesContent))
	mux.HandleFunc("POST "+PathSessions+"/{bios}/files/op", h.wrap(h.handleFilesOp))
	mux.HandleFunc("GET "+PathSessions+"/{bios}/trash", h.wrap(h.handleTrashList))
	mux.HandleFunc("POST "+PathSessions+"/{bios}/trash", h.wrap(h.handleTrashOp))
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
	info, ok := h.Info(r.PathValue("bios"))
	if !ok {
		return esxi.ErrGuestOffline()
	}
	return writeJSON(w, info)
}

func (h *Hub) handleHealth(w http.ResponseWriter, r *http.Request) error {
	out, err := h.Health(r.Context(), r.PathValue("bios"))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleProcsGet(w http.ResponseWriter, r *http.Request) error {
	out, err := h.Procs(r.Context(), r.PathValue("bios"))
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
	out, err := h.ProcAction(r.Context(), r.PathValue("bios"), body.Name, body.Action, confirmed(r, body.Confirm))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleServicesGet(w http.ResponseWriter, r *http.Request) error {
	out, err := h.Services(r.Context(), r.PathValue("bios"))
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
	out, err := h.ServiceAction(r.Context(), r.PathValue("bios"), body.Name, body.Action, confirmed(r, body.Confirm))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleExec(w http.ResponseWriter, r *http.Request) error {
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		return esxi.ErrBad("invalid json")
	}
	out, err := h.Exec(r.Context(), r.PathValue("bios"), req)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleTerm(w http.ResponseWriter, r *http.Request) error {
	out, err := h.IssueTerm(r.PathValue("bios"))
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
	out, err := h.Power(r.Context(), r.PathValue("bios"), body.Action, confirmed(r, body.Confirm))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleDisplayGet(w http.ResponseWriter, _ *http.Request) error {
	return writeJSON(w, DisplayInfo{
		ViewOnly: true,
		FPS:      DisplayFPS,
		MaxSide:  DisplayMaxSide,
		Note:     "POST for a one-shot " + PathDisplayWS + " ticket. JPEG ~2fps, max side 1920; no input. Does not fall back to hypervisor screenshot.",
	})
}

func (h *Hub) handleDisplayPost(w http.ResponseWriter, r *http.Request) error {
	out, err := h.IssueDisplay(r.PathValue("bios"))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleFilesList(w http.ResponseWriter, r *http.Request) error {
	out, err := h.FilesList(r.Context(), r.PathValue("bios"), r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleFilesContent(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	return h.WriteFile(w, ctx, r.PathValue("bios"), r.URL.Query().Get("path"))
}

func (h *Hub) handleFilesOp(w http.ResponseWriter, r *http.Request) error {
	var req FilesOpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return esxi.ErrBad("invalid json")
	}
	req.Confirm = confirmed(r, req.Confirm)
	out, err := h.FilesOp(r.Context(), r.PathValue("bios"), req)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleTrashList(w http.ResponseWriter, r *http.Request) error {
	out, err := h.Trash(r.Context(), r.PathValue("bios"), TrashRequest{Op: TrashList})
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleTrashOp(w http.ResponseWriter, r *http.Request) error {
	var req TrashRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return esxi.ErrBad("invalid json")
	}
	req.Confirm = confirmed(r, req.Confirm)
	out, err := h.Trash(r.Context(), r.PathValue("bios"), req)
	if err != nil {
		return err
	}
	return writeJSON(w, out)
}

func (h *Hub) handleFilesPut(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Query().Get("path")
	if path == "" {
		return esxi.ErrBad("path required")
	}
	qConfirm, _ := strconv.ParseBool(r.URL.Query().Get("confirm"))
	sha := strings.TrimSpace(r.Header.Get("X-Content-SHA256"))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	out, err := h.ReadFile(ctx, r.PathValue("bios"), path, r.Body, r.ContentLength, sha, confirmed(r, qConfirm))
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
	s := h.get(t.BiosUUID)
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
	if _, err := s.call(ctx, t.ID, MethodTermOpen, TermOpenRequest{ID: t.ID, Cols: cols, Rows: rows}); err != nil {
		s.detachTerm(sink)
		_ = browser.WriteMessage(websocket.TextMessage, []byte("term.open: "+err.Error()))
		return
	}
	defer func() {
		// Unregistered means the PTY is gone or now owned by a newer terminal.
		if !s.detachTerm(sink) {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), TermOpenTimeout)
		_, _ = s.call(c, t.ID+"-close", MethodTermClose, map[string]string{"id": t.ID})
		cancel()
	}()

	idle := time.NewTimer(TermIdle)
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
					c, cancel := context.WithTimeout(ctx, TermOpenTimeout)
					_, _ = s.call(c, t.ID+"-resize-"+strconv.FormatInt(time.Now().UnixNano(), 36), MethodTermResize, size)
					cancel()
					continue
				}
			}
			_ = s.write(Frame{ID: t.ID, Stream: "in", Data: data})
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
				idle.Reset(TermIdle)
			case <-sink.done:
				if sink.bye != "" {
					_ = browser.WriteMessage(websocket.BinaryMessage, []byte("\r\n\x1b[90m["+sink.bye+"]\x1b[0m\r\n"))
				}
				errc <- errOffline
				return
			case b := <-sink.out:
				idle.Reset(TermIdle)
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
func parseResize(data []byte) (TermOpenRequest, bool) {
	if len(data) < 8 || data[0] != '{' {
		return TermOpenRequest{}, false
	}
	var m struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if json.Unmarshal(data, &m) != nil || m.Type != "resize" || m.Cols <= 0 || m.Rows <= 0 {
		return TermOpenRequest{}, false
	}
	return TermOpenRequest{Cols: m.Cols, Rows: m.Rows}, true
}

func (h *Hub) HandleDisplayWS(w http.ResponseWriter, r *http.Request) {
	t, ok := h.takeTicket(r.URL.Query().Get("id"), "display")
	if !ok {
		http.Error(w, "ticket not found or already used", http.StatusGone)
		return
	}
	s := h.get(t.BiosUUID)
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
	if _, err := s.call(ctx, t.ID, MethodDisplayOpen, map[string]any{}); err != nil {
		_ = browser.WriteMessage(websocket.TextMessage, []byte("display.open: "+err.Error()))
		return
	}
	s.mu.Lock()
	s.displayID = t.ID
	s.mu.Unlock()
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), TermOpenTimeout)
		_, _ = s.call(c, t.ID+"-close", MethodDisplayClose, map[string]string{"id": t.ID})
		cancel()
		s.mu.Lock()
		s.displayID = ""
		s.mu.Unlock()
	}()

	idle := time.NewTimer(DisplayIdle)
	defer idle.Stop()
	errc := make(chan error, 2)
	go func() {
		for {
			if _, _, err := browser.ReadMessage(); err != nil {
				errc <- err
				return
			}
			// view-only: discard any client bytes
		}
	}()
	go func() {
		for {
			select {
			case <-s.closed:
				errc <- errOffline
				return
			case <-idle.C:
				errc <- context.DeadlineExceeded
				return
			case b, ok := <-s.displayOut:
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
				idle.Reset(DisplayIdle)
				if err := browser.WriteMessage(websocket.BinaryMessage, b); err != nil {
					errc <- err
					return
				}
			}
		}
	}()
	<-errc
}
