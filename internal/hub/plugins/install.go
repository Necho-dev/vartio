package plugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"vartio/internal/common"
)

var pluginName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

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

// Install downloads, builds and starts name. A plugin that is already running
// is left as it is. A previous failure is retried.
func (r *Registry) Install(ctx context.Context, name string) error {
	if !pluginName.MatchString(name) {
		return errNotFound(name)
	}
	r.mu.Lock()
	item, ok := r.catalogBy(name)
	if !ok {
		r.mu.Unlock()
		return errNotFound(name)
	}
	if r.running[name] != nil {
		r.mu.Unlock()
		return nil
	}
	if r.starting[name] {
		r.mu.Unlock()
		return &common.Error{Status: 503, Code: "plugin_installing", Message: "plugin " + name + " is still installing"}
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

	bin := filepath.Join(r.Dir, name, "vartio-plugin")
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		var err error
		bin, err = r.fetch.Fetch(ctx, item, filepath.Join(r.Dir, name))
		if err != nil {
			return fail(err)
		}
	}
	if err := r.start(ctx, item, bin); err != nil {
		return fail(err)
	}
	r.mu.Lock()
	r.upsertPin(item)
	r.mu.Unlock()
	return r.remember(name, true)
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
		Manual:    append([]gitEntry(nil), r.manual...),
		Repos:     append([]repoEntry(nil), r.repos...),
		Pins:      append([]gitEntry(nil), r.pins...),
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
