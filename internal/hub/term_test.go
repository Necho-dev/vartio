package hub

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"vartio/internal/common"
	"vartio/internal/entities"
)

func TestIssueTermContainer(t *testing.T) {
	h := NewHub("")
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http")
	agent := dialHello(t, base+"/v1/probe/connect", entities.Hello{BiosUUID: "dock-1", Caps: []string{entities.CapDocker}})
	waitOnline(t, h, "dock-1")
	_ = dialHello(t, base+"/v1/probe/connect", entities.Hello{BiosUUID: "plain-1"})
	waitOnline(t, h, "plain-1")

	code := func(err error) string {
		var api *common.Error
		if errors.As(err, &api) {
			return api.Code
		}
		return ""
	}
	for _, c := range []struct {
		key  string
		req  entities.TermOpenRequest
		want string
	}{
		{"dock-1", entities.TermOpenRequest{Mode: entities.TermExec, Container: "web"}, "agent_policy"},
		{"dock-1", entities.TermOpenRequest{Mode: entities.TermLogs}, "bad_request"},
		{"dock-1", entities.TermOpenRequest{Mode: "attach", Container: "web"}, "bad_request"},
		{"plain-1", entities.TermOpenRequest{Mode: entities.TermLogs, Container: "web"}, "cap_missing"},
	} {
		if _, err := h.IssueTerm(c.key, c.req); code(err) != c.want {
			t.Errorf("%s %+v: err %v, want %s", c.key, c.req, err, c.want)
		}
	}

	tk, err := h.IssueTerm("dock-1", entities.TermOpenRequest{Mode: entities.TermLogs, Container: " web ", Tail: 50, Cols: 999})
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan entities.TermOpenRequest, 1)
	go func() {
		for {
			var fr entities.Frame
			if agent.ReadJSON(&fr) != nil {
				return
			}
			if fr.Method == entities.MethodTermOpen {
				var req entities.TermOpenRequest
				_ = json.Unmarshal(fr.Params, &req)
				opened <- req
				b, _ := json.Marshal(entities.TermOpenResult{OK: true})
				_ = agent.WriteJSON(entities.Frame{ID: fr.ID, Result: b})
			}
		}
	}()
	browser, _, err := websocket.DefaultDialer.Dial(base+tk["ws"]+"&cols=100&rows=30", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	select {
	case req := <-opened:
		if req.Mode != entities.TermLogs || req.Container != "web" || req.Tail != 50 || req.Cols != 100 || req.Rows != 30 || req.ID == "" {
			t.Fatalf("term.open %+v", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent never got term.open")
	}
}
