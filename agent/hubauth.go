package agent

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"vartio/internal/entities"
)

// Hub authentication (see internal/guest/auth.go). Per control URL the agent keeps the
// hub's public key, pinned on first contact unless GUEST_HUB_KEY is set, and the
// device credential the hub issued. A hub that cannot sign with a trusted key is
// refused before it can send a single request.

type hubEntry struct {
	Key        string `json:"key,omitempty"`
	Device     string `json:"device,omitempty"`
	Credential string `json:"credential,omitempty"`
}

type hubStateFile struct {
	Hubs map[string]*hubEntry `json:"hubs"`
}

var hubStateMu sync.Mutex

// hubStatePath: GUEST_HUB_STATE, else GUEST_STATE_DIR in a container, else next to
// the binary (survives upgrades), else the user config dir.
func hubStatePath() string {
	if p := strings.TrimSpace(getenv("VARTIO_AGENT_HUB_STATE", "GUEST_HUB_STATE")); p != "" {
		return p
	}
	if inContainer() {
		return filepath.Join(stateDir(), "hub.json")
	}
	if exe, err := selfExe(); err == nil {
		dir := filepath.Dir(exe)
		legacy := filepath.Join(dir, "probe-agent.hub.json")
		next := filepath.Join(dir, "vartio-agent.hub.json")
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
		if _, err := os.Stat(next); err == nil || dirWritable(dir) {
			return next
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	legacy := filepath.Join(dir, "probe-agent", "hub.json")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return filepath.Join(dir, "vartio", "hub.json")
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".probe-agent-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// hubID keys the state by scheme, host and path of the control URL.
func hubID(control string) string {
	u, err := url.Parse(strings.TrimSpace(control))
	if err != nil {
		return strings.TrimSpace(control)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + u.Path
}

func readHubState(path string) hubStateFile {
	st := hubStateFile{Hubs: map[string]*hubEntry{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Hubs == nil {
		st.Hubs = map[string]*hubEntry{}
	}
	return st
}

func loadHubEntry(control string) hubEntry {
	hubStateMu.Lock()
	defer hubStateMu.Unlock()
	if e := readHubState(hubStatePath()).Hubs[hubID(control)]; e != nil {
		return *e
	}
	return hubEntry{}
}

func saveHubEntry(control string, e hubEntry) error {
	hubStateMu.Lock()
	defer hubStateMu.Unlock()
	path := hubStatePath()
	st := readHubState(path)
	st.Hubs[hubID(control)] = &e
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// deviceKey matches entities.Hello.Key() after the hub's normalization.
func (cfg config) deviceKey() string {
	if cfg.DeviceID != "" {
		return strings.ToLower(strings.TrimSpace(cfg.DeviceID))
	}
	return strings.ToLower(strings.TrimSpace(cfg.BiosUUID))
}

// setAgentAuth adds GUEST_TOKEN and, when this device holds one, its credential.
func (cfg config) setAgentAuth(h http.Header, e hubEntry) {
	if cfg.Token != "" {
		h.Set("X-Guest-Token", cfg.Token)
	}
	if dev := cfg.deviceKey(); e.Credential != "" && e.Device == dev {
		h.Set(entities.HeaderDevice, dev)
		h.Set(entities.HeaderCredential, e.Credential)
	}
}

func hubVerifyOff() bool {
	return strings.EqualFold(strings.TrimSpace(getenv("VARTIO_AGENT_HUB_VERIFY", "GUEST_HUB_VERIFY")), "off")
}

// trustedHubKeys is GUEST_HUB_KEY (comma-separated, for key rotation) or the pinned key.
func trustedHubKeys(e hubEntry) (keys []string, fromEnv bool) {
	for _, k := range strings.Split(getenv("VARTIO_AGENT_HUB_KEY", "GUEST_HUB_KEY"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) > 0 {
		return keys, true
	}
	if e.Key != "" {
		return []string{e.Key}, false
	}
	return nil, false
}

var errHubIdentity = errors.New("hub identity check failed")

// acceptHello verifies HelloOK against the trusted hub key and folds a pinned key or a
// new credential into e. changed tells the caller to persist e.
func acceptHello(e *hubEntry, nonce, device string, ack entities.HelloOK) (changed bool, err error) {
	if !hubVerifyOff() {
		trusted, fromEnv := trustedHubKeys(*e)
		if ack.Sig == "" || ack.HubKey == "" {
			if len(trusted) > 0 {
				return false, fmt.Errorf("%w: hub did not sign the hello but key %s is trusted (%s)", errHubIdentity, shortKey(trusted[0]), hubTrustHint(fromEnv))
			}
			return false, nil
		}
		if len(trusted) > 0 && !slices.Contains(trusted, ack.HubKey) {
			return false, fmt.Errorf("%w: hub key %s is not the trusted %s (%s)", errHubIdentity, shortKey(ack.HubKey), shortKey(trusted[0]), hubTrustHint(fromEnv))
		}
		pub, err1 := base64.StdEncoding.DecodeString(ack.HubKey)
		sig, err2 := base64.StdEncoding.DecodeString(ack.Sig)
		if err1 != nil || err2 != nil || len(pub) != ed25519.PublicKeySize ||
			!ed25519.Verify(pub, entities.HelloSigMessage(nonce, device, ack.Credential), sig) {
			return false, fmt.Errorf("%w: bad hello signature", errHubIdentity)
		}
		if len(trusted) == 0 {
			e.Key, changed = ack.HubKey, true
			log.Printf("pinned hub key %s", shortKey(ack.HubKey))
		}
	}
	if ack.Credential != "" {
		e.Device, e.Credential, changed = device, ack.Credential, true
	}
	return changed, nil
}

func hubTrustHint(fromEnv bool) string {
	if fromEnv {
		return "GUEST_HUB_KEY"
	}
	return "pinned in " + hubStatePath() + "; delete it there if the hub key was replaced on purpose"
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:12] + "…"
	}
	return k
}
