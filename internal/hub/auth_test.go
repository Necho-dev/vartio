package hub

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"vartio/internal/entities"

	"github.com/gorilla/websocket"
)

func dialAuth(t *testing.T, url string, hdr http.Header, hello entities.Hello) (entities.HelloOK, *http.Response, error) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(url, hdr)
	if err != nil {
		return entities.HelloOK{}, resp, err
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	var ack entities.HelloOK
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	if ack.OK {
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
	}
	return ack, resp, nil
}

func verifyAck(t *testing.T, ack entities.HelloOK, pub, nonce, device string) {
	t.Helper()
	if ack.HubKey != pub {
		t.Fatalf("hub key %q, want %q", ack.HubKey, pub)
	}
	k, _ := base64.StdEncoding.DecodeString(ack.HubKey)
	sig, _ := base64.StdEncoding.DecodeString(ack.Sig)
	if !ed25519.Verify(k, entities.HelloSigMessage(nonce, device, ack.Credential), sig) {
		t.Fatal("hello signature does not verify")
	}
}

func TestHubAuthCredentialLifecycle(t *testing.T) {
	dir := t.TempDir()
	keyPath, credPath := filepath.Join(dir, "hub.key"), filepath.Join(dir, "agents.json")
	auth, err := LoadAuth(keyPath, credPath)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub("fleet")
	h.Auth = auth
	srv := httptest.NewServer(h.DevHandler())
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + entities.PathConnect
	pub := auth.PublicKey()
	token := http.Header{"X-Guest-Token": {"fleet"}}
	hello := entities.Hello{BiosUUID: "b1", DeviceID: "dev1", Nonce: "n1"}

	// Enroll with the fleet token; an unconfirmed credential is simply replaced.
	ack, _, err := dialAuth(t, url, token, hello)
	if err != nil || !ack.OK || ack.Credential == "" {
		t.Fatalf("enroll: %v %+v", err, ack)
	}
	verifyAck(t, ack, pub, "n1", "dev1")
	h.Kick("dev1")
	waitOffline(t, h, "dev1")
	ack, _, _ = dialAuth(t, url, token, hello)
	cred := ack.Credential
	if !ack.OK || cred == "" || auth.State("dev1") != CredPending {
		t.Fatalf("re-enroll %+v state %q", ack, auth.State("dev1"))
	}
	h.Kick("dev1")
	waitOffline(t, h, "dev1")

	// Presenting it (no token needed) activates it.
	byCred := http.Header{entities.HeaderDevice: {"dev1"}, entities.HeaderCredential: {cred}}
	hello.Nonce = "n2"
	ack, _, err = dialAuth(t, url, byCred, hello)
	if err != nil || !ack.OK || ack.Credential != "" || auth.State("dev1") != CredActive {
		t.Fatalf("credential login: %v %+v %q", err, ack, auth.State("dev1"))
	}
	verifyAck(t, ack, pub, "n2", "dev1")
	h.Kick("dev1")
	waitOffline(t, h, "dev1")

	// The token alone can no longer claim the device id.
	if ack, _, _ := dialAuth(t, url, token, hello); ack.OK || ack.Code != "credential_required" {
		t.Fatalf("token-only on bound device: %+v", ack)
	}
	// A wrong credential without the token is refused at the HTTP upgrade.
	if _, resp, err := dialAuth(t, url, http.Header{entities.HeaderDevice: {"dev1"}, entities.HeaderCredential: {"nope"}}, hello); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong credential: %v", err)
	}
	// A credential for dev1 does not authenticate another device id.
	if ack, _, _ := dialAuth(t, url, byCred, entities.Hello{BiosUUID: "b2", DeviceID: "dev2", Nonce: "x"}); ack.OK || ack.Code != "unauthorized" {
		t.Fatalf("credential for other id: %+v", ack)
	}

	// Rotating GUEST_TOKEN does not affect bound devices.
	h.Token = "fleet2"
	if ack, _, err := dialAuth(t, url, byCred, hello); err != nil || !ack.OK {
		t.Fatalf("after token rotation: %v %+v", err, ack)
	}
	h.Kick("dev1")
	waitOffline(t, h, "dev1")
	h.Token = "fleet"

	// Old agents (no nonce) keep the token-only flow and get nothing new.
	if ack, _, _ := dialAuth(t, url, token, entities.Hello{BiosUUID: "old"}); !ack.OK || ack.Credential != "" || ack.Sig != "" {
		t.Fatalf("legacy agent: %+v", ack)
	}

	auth.Enroll = false
	if ack, _, _ := dialAuth(t, url, token, entities.Hello{BiosUUID: "b3", DeviceID: "dev3", Nonce: "y"}); ack.OK || ack.Code != "enrollment_disabled" {
		t.Fatalf("enroll off: %+v", ack)
	}
	auth.Enroll = true

	// Reset: the next token hello enrolls again.
	if !h.ForgetCredential("dev1") || auth.State("dev1") != "" {
		t.Fatal("forget")
	}
	if ack, _, _ := dialAuth(t, url, token, hello); !ack.OK || ack.Credential == "" {
		t.Fatalf("after reset: %+v", ack)
	}
	h.Kick("dev1")
	waitOffline(t, h, "dev1")

	// Key and credentials survive a restart.
	again, err := LoadAuth(keyPath, credPath)
	if err != nil {
		t.Fatal(err)
	}
	if again.PublicKey() != pub || again.State("dev1") != CredPending {
		t.Fatalf("reload: key %v state %q", again.PublicKey() == pub, again.State("dev1"))
	}
}
