package guest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"

	"govmomi-wmks/internal/esxi"
	"govmomi-wmks/internal/release"
)

// Releases serves signed agent builds from the dist layout
// <Dir>/<version>/manifest.json; <Dir>/latest (a symlink) picks what is offered.
type Releases struct {
	Dir string
}

var versionRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

func (rs *Releases) Get(version string) (*release.Manifest, error) {
	if rs == nil || rs.Dir == "" {
		return nil, errors.New("no release directory configured")
	}
	if !versionRe.MatchString(version) {
		return nil, fmt.Errorf("invalid version %q", version)
	}
	b, err := os.ReadFile(filepath.Join(rs.Dir, version, release.ManifestName))
	if err != nil {
		return nil, fmt.Errorf("version %s has no signed manifest", version)
	}
	var m release.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("version %s: %v", version, err)
	}
	if m.Version != version {
		return nil, fmt.Errorf("version %s: manifest says %s", version, m.Version)
	}
	return &m, nil
}

// List returns every signed version, newest first.
func (rs *Releases) List() []*release.Manifest {
	if rs == nil || rs.Dir == "" {
		return nil
	}
	ents, _ := os.ReadDir(rs.Dir)
	var out []*release.Manifest
	for _, e := range ents {
		if !e.IsDir() || !versionRe.MatchString(e.Name()) {
			continue
		}
		if m, err := rs.Get(e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return release.Compare(out[i].Version, out[j].Version) > 0 })
	return out
}

// Latest is the version <Dir>/latest points at, else the highest signed one.
func (rs *Releases) Latest() *release.Manifest {
	if rs == nil || rs.Dir == "" {
		return nil
	}
	if target, err := os.Readlink(filepath.Join(rs.Dir, "latest")); err == nil {
		if m, err := rs.Get(filepath.Base(target)); err == nil {
			return m
		}
	}
	if list := rs.List(); len(list) > 0 {
		return list[0]
	}
	return nil
}

func downloadPath(version, goos, arch string) string {
	return PathAgentDownload + "/" + version + "/" + goos + "/" + arch
}

func (h *Hub) agentAuthed(w http.ResponseWriter, r *http.Request) bool {
	if tokenOK(h.Token, guestToken(r)) {
		return true
	}
	writeAPIErr(w, &esxi.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "invalid or missing guest token"})
	return false
}

// handleAgentLatest answers 204 when the caller is already current.
func (h *Hub) handleAgentLatest(w http.ResponseWriter, r *http.Request) {
	if !h.agentAuthed(w, r) {
		return
	}
	q := r.URL.Query()
	m := h.Releases.Latest()
	if m == nil || release.Compare(m.Version, q.Get("current")) <= 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a := m.Find(q.Get("os"), q.Get("arch"))
	if a == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_ = writeJSON(w, AgentLatest{Version: m.Version, Artifact: *a, URL: downloadPath(m.Version, a.OS, a.Arch)})
}

func (h *Hub) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if !h.agentAuthed(w, r) {
		return
	}
	m, err := h.Releases.Get(r.PathValue("version"))
	if err != nil {
		writeAPIErr(w, &esxi.Error{Status: http.StatusNotFound, Code: "not_found", Message: err.Error()})
		return
	}
	a := m.Find(r.PathValue("os"), r.PathValue("arch"))
	if a == nil {
		writeAPIErr(w, &esxi.Error{Status: http.StatusNotFound, Code: "not_found", Message: "no build for this platform"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filepath.Join(h.Releases.Dir, m.Version, filepath.FromSlash(a.File)))
}

// ReleaseView is one offered version for the UI.
type ReleaseView struct {
	Version   string    `json:"version"`
	Created   time.Time `json:"created"`
	Platforms []string  `json:"platforms"`
}

func (h *Hub) handleReleases(w http.ResponseWriter, r *http.Request) error {
	out := struct {
		Latest   string        `json:"latest,omitempty"`
		Versions []ReleaseView `json:"versions"`
	}{Versions: []ReleaseView{}}
	if m := h.Releases.Latest(); m != nil {
		out.Latest = m.Version
	}
	for _, m := range h.Releases.List() {
		v := ReleaseView{Version: m.Version, Created: m.Created}
		for _, a := range m.Artifacts {
			v.Platforms = append(v.Platforms, a.OS+"/"+a.Arch)
		}
		out.Versions = append(out.Versions, v)
	}
	return writeJSON(w, out)
}

// upgradeKeep is how long finished upgrades stay visible in GET /upgrade.
const upgradeKeep = 30 * time.Minute

// progress updates the device's in-flight upgrade, if any.
func (h *Hub) progress(key string, fn func(*UpgradeProgress)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.upgrades[key]; p != nil && !upgradeFinal(p.Stage) {
		fn(p)
		p.Updated = time.Now()
	}
}

func upgradeFinal(stage string) bool {
	return stage == UpgradeDone || stage == UpgradeFailed || stage == UpgradeRolledBack
}

// settleUpgrade closes an upgrade waiting for the restart once the agent says hello again.
func (h *Hub) settleUpgrade(key string, hello Hello) {
	h.progress(key, func(p *UpgradeProgress) {
		if p.Stage != UpgradeRestart {
			return
		}
		switch r := hello.Upgrade; {
		case r != nil && !r.OK:
			p.Stage, p.Message = UpgradeRolledBack, r.Error
		case hello.Version == p.To:
			p.Stage = UpgradeDone
		}
	})
}

// Upgrades lists recent upgrades, newest first.
func (h *Hub) Upgrades() []UpgradeProgress {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]UpgradeProgress, 0, len(h.upgrades))
	for k, p := range h.upgrades {
		if time.Since(p.Updated) > upgradeKeep {
			delete(h.upgrades, k)
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

func (h *Hub) handleUpgrades(w http.ResponseWriter, r *http.Request) error {
	return writeJSON(w, h.Upgrades())
}

// Upgrade asks one agent to install version ("" = latest) and tracks it in Upgrades.
func (h *Hub) Upgrade(ctx context.Context, key, version string) (UpgradeResult, error) {
	s, err := h.lookup(key)
	if err != nil {
		return UpgradeResult{}, err
	}
	if s == nil {
		return UpgradeResult{}, esxi.ErrGuestOffline()
	}
	id := s.hello.Key()
	now := time.Now()
	h.mu.Lock()
	if p := h.upgrades[id]; p != nil && !upgradeFinal(p.Stage) && time.Since(p.Updated) < 5*time.Minute {
		h.mu.Unlock()
		return UpgradeResult{}, &esxi.Error{Status: http.StatusConflict, Code: "upgrade_busy", Message: "该主机已有升级在进行"}
	}
	h.upgrades[id] = &UpgradeProgress{ID: id, From: s.hello.Version, To: version, Stage: UpgradeQueued, Started: now, Updated: now}
	h.mu.Unlock()
	res, err := h.upgrade(ctx, s, version)
	h.progress(id, func(p *UpgradeProgress) {
		if res.To != "" {
			p.To = res.To
		}
		if err != nil {
			p.Stage, p.Message = UpgradeFailed, err.Error()
			var api *esxi.Error
			if errors.As(err, &api) {
				p.Message = api.Message
			}
			return
		}
		p.Stage, p.Done = UpgradeRestart, p.Total
	})
	return res, err
}

func (h *Hub) upgrade(ctx context.Context, s *session, version string) (UpgradeResult, error) {
	key := s.hello.Key()
	hello := s.hello
	if !hello.Has(CapUpgrade) {
		return UpgradeResult{}, &esxi.Error{Status: http.StatusConflict, Code: "upgrade_unsupported",
			Message: fmt.Sprintf("agent %s 不支持远程升级，需手动更新一次到支持远程升级的版本", hello.Version)}
	}
	var m *release.Manifest
	var err error
	if version == "" {
		if m = h.Releases.Latest(); m == nil {
			return UpgradeResult{}, &esxi.Error{Status: http.StatusNotFound, Code: "not_found", Message: "hub 上没有已签名的 agent 版本"}
		}
	} else if m, err = h.Releases.Get(version); err != nil {
		return UpgradeResult{}, &esxi.Error{Status: http.StatusNotFound, Code: "not_found", Message: err.Error()}
	}
	if m.Version == hello.Version {
		return UpgradeResult{}, &esxi.Error{Status: http.StatusConflict, Code: "already_current", Message: "已是 " + m.Version}
	}
	a := m.Find(hello.OS, hello.Arch)
	if a == nil {
		return UpgradeResult{}, &esxi.Error{Status: http.StatusNotFound, Code: "not_found",
			Message: fmt.Sprintf("%s 没有 %s/%s 的构建", m.Version, hello.OS, hello.Arch)}
	}
	h.progress(key, func(p *UpgradeProgress) { p.To, p.Stage, p.Total = m.Version, UpgradeDownload, a.Size })
	raw, err := h.Call(ctx, key, MethodUpgrade, UpgradeRequest{Version: m.Version, Artifact: *a, URL: downloadPath(m.Version, a.OS, a.Arch), Mode: "push"})
	if err != nil {
		return UpgradeResult{}, err
	}
	var res UpgradeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return UpgradeResult{}, err
	}
	if !res.OK {
		return res, &esxi.Error{Status: http.StatusBadGateway, Code: "upgrade_failed", Message: res.Message}
	}
	return res, nil
}

func (h *Hub) handleUpgrade(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Version string `json:"version"`
		Confirm bool   `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !confirmed(r, body.Confirm) {
		return &esxi.Error{Status: http.StatusForbidden, Code: "confirm_required", Message: "upgrade requires confirm=true"}
	}
	from := h.versionOf(r.PathValue("id"))
	res, err := h.Upgrade(r.Context(), r.PathValue("id"), body.Version)
	h.record(r, "agent.upgrade", upgradeNote(from, res.To, body.Version), err)
	if err != nil {
		return err
	}
	return writeJSON(w, res)
}

// BatchResult is one agent's outcome in a fleet upgrade.
type BatchResult struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Message string `json:"message,omitempty"`
}

// handleUpgradeBatch upgrades the given ids, or every online agent that is behind
// and supports upgrades when ids is empty; at most 4 at a time.
func (h *Hub) handleUpgradeBatch(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		IDs     []string `json:"ids"`
		Version string   `json:"version"`
		Confirm bool     `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !confirmed(r, body.Confirm) {
		return &esxi.Error{Status: http.StatusForbidden, Code: "confirm_required", Message: "upgrade requires confirm=true"}
	}
	ids := body.IDs
	if len(ids) == 0 {
		target := body.Version
		if target == "" {
			if m := h.Releases.Latest(); m != nil {
				target = m.Version
			}
		}
		for _, s := range h.List() {
			if hasCap(s.Caps, CapUpgrade) && target != "" && release.Compare(s.Version, target) < 0 {
				ids = append(ids, s.DeviceID)
			}
		}
	}
	out := make([]BatchResult, len(ids))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			from := h.versionOf(id)
			res, err := h.Upgrade(r.Context(), id, body.Version)
			h.audit(id, "agent.upgrade", upgradeNote(from, res.To, body.Version)+"（批量）", clientIP(r), err)
			out[i] = BatchResult{ID: id, OK: err == nil, From: from, To: res.To}
			if err != nil {
				out[i].Message = err.Error()
				var api *esxi.Error
				if errors.As(err, &api) {
					out[i].Message = api.Message
				}
			}
		}(i, id)
	}
	wg.Wait()
	return writeJSON(w, out)
}

func (h *Hub) versionOf(key string) string {
	if info, ok := h.Info(key); ok {
		return info.Version
	}
	return ""
}

func upgradeNote(from, to, asked string) string {
	if to == "" {
		to = asked
	}
	if to == "" {
		to = "最新版"
	}
	return from + " → " + to
}

func hasCap(caps []string, c string) bool {
	return slices.Contains(caps, c)
}
