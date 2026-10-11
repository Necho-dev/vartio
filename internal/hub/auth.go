package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vartio/internal/common"
	"vartio/internal/entities"
)

// Auth adds two things on top of the shared GUEST_TOKEN:
//
//   - The hub signs every hello with an Ed25519 key. Agents pin the key on first
//     contact (or take it from GUEST_HUB_KEY) and refuse a hub that cannot sign, so
//     knowing GUEST_TOKEN is not enough to impersonate the hub and drive terminals.
//   - Each device gets its own credential on first connect. Once the agent has shown
//     it back, that device id only connects with it: GUEST_TOKEN alone can neither take
//     over the device id nor is it needed any more, so it can be rotated. With Enroll
//     off, GUEST_TOKEN no longer admits new devices.
//
// An agent signals support by sending Hello.Nonce; older agents keep the token-only flow.
type Auth struct {
	Enroll bool

	key  ed25519.PrivateKey
	mu   sync.Mutex
	path string
	devs map[string]*credential
}

// Credential states as reported to the UI.
const (
	CredActive  = "active"  // the agent has presented its credential; token-only hellos are refused
	CredPending = "pending" // issued, not yet presented back
)

type credential struct {
	Hash    string    `json:"hash"`
	Active  bool      `json:"active"`
	Created time.Time `json:"created"`
	Seen    time.Time `json:"seen,omitzero"`
}

type credFile struct {
	Devices map[string]*credential `json:"devices"`
}

// LoadAuth reads (or creates) the hub signing key and the device credential file.
func LoadAuth(keyPath, credPath string) (*Auth, error) {
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	a := &Auth{Enroll: true, key: key, path: credPath, devs: map[string]*credential{}}
	b, err := os.ReadFile(credPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var f credFile
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", credPath, err)
		}
		for id, c := range f.Devices {
			if c != nil && c.Hash != "" {
				a.devs[normUUID(id)] = c
			}
		}
	}
	return a, nil
}

// The key file holds the base64 Ed25519 seed. Losing it locks out every agent that
// pinned the old public key, so it lives next to the other hub state and must be backed up.
func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s: not a base64 ed25519 seed", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return nil, err
	}
	log.Printf("hub signing key created at %s (back it up: agents pin its public key)", path)
	return priv, nil
}

// PublicKey is the base64 Ed25519 public key agents pin (GUEST_HUB_KEY).
func (a *Auth) PublicKey() string {
	return base64.StdEncoding.EncodeToString(a.key.Public().(ed25519.PublicKey))
}

func (a *Auth) sign(nonce, device, cred string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(a.key, entities.HelloSigMessage(nonce, device, cred)))
}

func hashCred(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Valid reports whether secret is the credential issued to device.
func (a *Auth) Valid(device, secret string) bool {
	device, secret = normUUID(device), strings.TrimSpace(secret)
	if device == "" || secret == "" {
		return false
	}
	a.mu.Lock()
	c := a.devs[device]
	a.mu.Unlock()
	return c != nil && subtle.ConstantTimeCompare([]byte(c.Hash), []byte(hashCred(secret))) == 1
}

// State is CredActive, CredPending or "" (no credential: legacy agent or never connected).
func (a *Auth) State(device string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.devs[normUUID(device)]
	switch {
	case c == nil:
		return ""
	case c.Active:
		return CredActive
	default:
		return CredPending
	}
}

// issue hands out a fresh pending credential, replacing an unconfirmed one.
func (a *Auth) issue(device string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	a.mu.Lock()
	a.devs[normUUID(device)] = &credential{Hash: hashCred(secret), Created: time.Now().UTC()}
	a.saveLocked()
	a.mu.Unlock()
	return secret, nil
}

func (a *Auth) activate(device string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.devs[normUUID(device)]
	if c == nil {
		return
	}
	first := !c.Active
	c.Active, c.Seen = true, time.Now().UTC()
	if first {
		a.saveLocked()
	}
}

// Forget drops the device's credential; its next hello enrolls again with GUEST_TOKEN.
func (a *Auth) Forget(device string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	device = normUUID(device)
	if a.devs[device] == nil {
		return false
	}
	delete(a.devs, device)
	a.saveLocked()
	return true
}

func (a *Auth) saveLocked() {
	if a.path == "" {
		return
	}
	b, _ := json.MarshalIndent(credFile{Devices: a.devs}, "", "  ")
	if dir := filepath.Dir(a.path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("save agent credentials: %v", err)
		return
	}
	if err := os.Rename(tmp, a.path); err != nil {
		log.Printf("save agent credentials: %v", err)
	}
}

// agentAuth is how an agent HTTP request (connect, release download) authenticated.
type agentAuth struct {
	token      bool   // GUEST_TOKEN matched
	credDevice string // device whose credential matched
}

func (a agentAuth) ok() bool { return a.token || a.credDevice != "" }

func (h *Hub) agentAuthorized(r *http.Request) agentAuth {
	var out agentAuth
	if h.Auth != nil {
		if dev := normUUID(r.Header.Get(entities.HeaderDevice)); h.Auth.Valid(dev, r.Header.Get(entities.HeaderCredential)) {
			out.credDevice = dev
		}
	}
	out.token = tokenOK(h.Token, guestToken(r))
	return out
}

// helloAuth decides on a hello after the WebSocket upgrade. It returns the HelloOK to
// send, or an RPCError when the hello must be refused.
func (h *Hub) helloAuth(hello entities.Hello, auth agentAuth) (entities.HelloOK, error) {
	ok := entities.HelloOK{OK: true}
	if h.Auth == nil {
		return ok, nil
	}
	key := hello.Key()
	byCred := auth.credDevice != "" && auth.credDevice == key
	// An agent whose fingerprint changed still sends its old device's credential;
	// the new id is then judged by GUEST_TOKEN like any new device.
	if !byCred && !auth.token {
		return ok, &RPCError{Code: "unauthorized", Message: "credential belongs to another device id"}
	}
	if !byCred {
		switch h.Auth.State(key) {
		case CredActive:
			return ok, &RPCError{Code: "credential_required", Message: "device " + key + " is bound to a credential; reset it on the hub to enroll again"}
		case "":
			if !h.Auth.Enroll {
				return ok, &RPCError{Code: "enrollment_disabled", Message: "new devices cannot join with GUEST_TOKEN (PROBE_HUB_ENROLL=off)"}
			}
		}
	}
	if hello.Nonce == "" {
		return ok, nil
	}
	if byCred {
		h.Auth.activate(key)
	} else {
		cred, err := h.Auth.issue(key)
		if err != nil {
			return ok, &RPCError{Code: "internal", Message: err.Error()}
		}
		ok.Credential = cred
	}
	ok.HubKey = h.Auth.PublicKey()
	ok.Sig = h.Auth.sign(hello.Nonce, key, ok.Credential)
	return ok, nil
}

func (h *Hub) handleHubKey(w http.ResponseWriter, r *http.Request) error {
	if h.Auth == nil {
		return writeJSON(w, map[string]any{"enabled": false})
	}
	return writeJSON(w, map[string]any{"enabled": true, "publicKey": h.Auth.PublicKey(), "enroll": h.Auth.Enroll})
}

func (h *Hub) handleCredentialReset(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !confirmed(r, body.Confirm) {
		return &common.Error{Status: http.StatusForbidden, Code: "confirm_required", Message: "credential reset requires confirm=true"}
	}
	if h.Auth == nil {
		return &common.Error{Status: http.StatusConflict, Code: "auth_disabled", Message: "hub has no device credentials"}
	}
	key := normUUID(r.PathValue("id"))
	removed := h.Auth.Forget(key)
	h.record(r, "credential.reset", "", nil)
	return writeJSON(w, map[string]any{"id": key, "removed": removed})
}

// CredentialState is the device's credential state for the UI ("" without Auth).
func (h *Hub) CredentialState(key string) string {
	if h.Auth == nil {
		return ""
	}
	if st := h.Auth.State(key); st != "" {
		return st
	}
	return "none"
}

// ForgetCredential drops a device's credential (re-enroll after the agent lost it).
func (h *Hub) ForgetCredential(key string) bool {
	return h.Auth != nil && h.Auth.Forget(key)
}
