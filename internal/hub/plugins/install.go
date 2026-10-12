package plugins

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	sdk "github.com/Necho-dev/vartio/sdk"
	"vartio/internal/common"
)

var pluginName = sdk.PluginName

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Restore downloads and starts every plugin recorded as installed.
func (r *Registry) Restore(ctx context.Context) {
	r.refreshSources(ctx)
	r.mu.Lock()
	names := append([]string(nil), r.wanted...)
	r.mu.Unlock()
	for _, name := range names {
		if err := r.Install(ctx, name); err != nil {
			log.Printf("plugin %s: %v", name, err)
		}
	}
}

// Install downloads a signed package and starts it. A plugin already running
// the same package is left alone.
func (r *Registry) Install(ctx context.Context, name string) error {
	if !pluginName.MatchString(name) {
		return errNotFound(name)
	}
	r.mu.Lock()
	item, ok := r.lookup(name)
	if !ok {
		r.mu.Unlock()
		return errNotFound(name)
	}
	art, err := pickArtifact(item)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	pub, trusted := r.keys[art.SigningKeyID]
	var key ed25519.PublicKey
	if trusted {
		key = ed25519.PublicKey(bytes.Clone(pub))
	}
	pinSHA := ""
	for _, p := range r.pins {
		if p.Name == name {
			pinSHA = p.SHA256
		}
	}
	runningSame := r.running[name] != nil && r.running[name].manifest.Version == item.Version && pinSHA == art.SHA256
	if runningSame {
		r.mu.Unlock()
		return nil
	}
	if r.starting[name] {
		r.mu.Unlock()
		return &common.Error{Status: 503, Code: "plugin_installing", Message: "plugin " + name + " is still installing"}
	}
	if !trusted {
		r.mu.Unlock()
		return common.ErrBad("不认识的签名密钥 %s", art.SigningKeyID)
	}
	r.starting[name] = true
	delete(r.problems, name)
	r.mu.Unlock()

	fail := func(err error) error {
		r.mu.Lock()
		r.starting[name] = false
		r.problems[name] = err.Error()
		r.mu.Unlock()
		return err
	}

	dir := filepath.Join(r.Dir, name, item.Version)
	bin := filepath.Join(dir, sdk.BinaryName)
	if pinSHA != art.SHA256 || !localOK(dir, key, art.SigningKeyID, item.Manifest) {
		r.stopName(name)
		body, err := downloadPackage(ctx, art.URL, art.Size)
		if err != nil {
			return fail(err)
		}
		if packageSHA(body) != art.SHA256 {
			return fail(common.ErrBad("安装包 SHA-256 不符"))
		}
		pkg, err := sdk.Open(body)
		if err != nil {
			return fail(common.ErrBad("安装包无法打开"))
		}
		got, err := pkg.Verify(key, art.SigningKeyID)
		if err != nil {
			return fail(common.ErrBad("安装包签名校验失败"))
		}
		if got.Name != name || !sdk.SameManifest(got, item.Manifest) {
			return fail(common.ErrBad("安装包清单与目录不一致"))
		}
		if err := writePackage(dir, pkg); err != nil {
			return fail(err)
		}
	}
	if err := r.start(ctx, item, bin); err != nil {
		return fail(err)
	}
	r.mu.Lock()
	r.upsertPin(item, art)
	r.mu.Unlock()
	r.keepOnly(name, item.Version)
	return r.remember(name, true)
}

func (r *Registry) stopName(name string) {
	r.mu.Lock()
	p := r.running[name]
	delete(r.running, name)
	r.mu.Unlock()
	if p != nil {
		p.stop()
		r.hub.CloseTunnels("plugin:" + name)
	}
}

// Uninstall stops the process, deletes the downloaded tree, and forgets it.
func (r *Registry) Uninstall(name string) error {
	if !pluginName.MatchString(name) {
		return errNotFound(name)
	}
	r.mu.Lock()
	_, known := r.catalogBy(name)
	wanted := contains(r.wanted, name)
	p := r.running[name]
	if !known && !wanted && p == nil {
		r.mu.Unlock()
		return errNotFound(name)
	}
	delete(r.running, name)
	delete(r.problems, name)
	r.starting[name] = false
	r.mu.Unlock()
	if p != nil {
		p.stop()
		r.hub.CloseTunnels("plugin:" + name)
	}
	_ = os.RemoveAll(filepath.Join(r.Dir, name))
	log.Printf("plugin %s uninstalled", name)
	return r.remember(name, false)
}

func (r *Registry) remember(name string, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.wanted[:0:0]
	seen := false
	for _, n := range r.wanted {
		if n == name {
			seen = true
			if on {
				next = append(next, n)
			}
			continue
		}
		next = append(next, n)
	}
	if on && !seen {
		next = append(next, name)
	}
	r.wanted = next
	if !on {
		r.dropPin(name)
	}
	return r.saveLocked()
}

func (r *Registry) saveLocked() error {
	if r.statePath == "" {
		return nil
	}
	st := stateFile{
		Installed: append([]string(nil), r.wanted...),
		Manual:    append([]manualEntry(nil), r.manual...),
		Repos:     append([]repoEntry(nil), r.repos...),
		Pins:      append([]pinEntry(nil), r.pins...),
		Configs:   append([]configEntry(nil), r.configs...),
	}
	if st.Installed == nil {
		st.Installed = []string{}
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(r.statePath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := r.statePath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.statePath)
}
