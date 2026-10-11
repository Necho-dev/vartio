package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/Necho-dev/vartio/sdk"
	"vartio/internal/common"
)

// A catalog is JSON, catalogVersion 1. Each plugin lists signed .vtp artifacts.
// The hub trusts a public key because an operator added it, not because the
// catalog names a key id.

const catalogVersion = 1

type manualEntry struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	SHA256       string   `json:"sha256"`
	SigningKeyID string   `json:"signingKeyId"`
	Size         int64    `json:"size,omitempty"`
	Title        string   `json:"title,omitempty"`
	Version      string   `json:"version,omitempty"`
	Description  string   `json:"description,omitempty"`
	Source       string   `json:"source,omitempty"`
	Primitives   []string `json:"primitives,omitempty"`
	Targets      []string `json:"targets,omitempty"`
	Machines     []string `json:"machines,omitempty"`
	UI           bool     `json:"ui,omitempty"`
}

type repoEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
}

type pinEntry struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	Target       string `json:"target,omitempty"`
	URL          string `json:"url,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	SigningKeyID string `json:"signingKeyId,omitempty"`
	Size         int64  `json:"size,omitempty"`
	Git          string `json:"git,omitempty"`
}

type catalogDoc struct {
	CatalogVersion int `json:"catalogVersion"`
	Repository     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"repository"`
	Plugins []catalogPlugin `json:"plugins"`
}

type catalogPlugin struct {
	sdk.Manifest
	Artifacts []Artifact `json:"artifacts"`
}

func validateFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	host := ""
	if u != nil {
		host = u.Hostname()
	}
	local := host == "localhost" || host == "127.0.0.1"
	if err != nil || u == nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && local)) {
		return nil, common.ErrBad("地址需要是 https URL")
	}
	return u, nil
}

func normalizeSHA(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !sha256Hex.MatchString(s) {
		return "", common.ErrBad("SHA-256 需要是 64 位十六进制")
	}
	return s, nil
}

func keepArtifact(a Artifact, keyID string) (Artifact, bool) {
	a.Target = strings.TrimSpace(a.Target)
	a.URL = strings.TrimSpace(a.URL)
	a.SigningKeyID = strings.TrimSpace(a.SigningKeyID)
	sum, err := normalizeSHA(a.SHA256)
	if err != nil || !targetName.MatchString(a.Target) || a.Size < 0 {
		return Artifact{}, false
	}
	if _, err := validateFetchURL(a.URL); err != nil {
		return Artifact{}, false
	}
	if keyID != "" && a.SigningKeyID != keyID {
		return Artifact{}, false
	}
	if a.SigningKeyID == "" {
		return Artifact{}, false
	}
	a.SHA256 = sum
	return a, true
}

func parseCatalog(body []byte, repo repoEntry, official bool) ([]CatalogItem, error) {
	var doc catalogDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, common.ErrBad("目录不是 JSON")
	}
	if doc.CatalogVersion != catalogVersion {
		return nil, common.ErrBad("目录版本需要是 %d", catalogVersion)
	}
	if id := strings.TrimSpace(doc.Repository.ID); id != "" && repo.ID != "" && id != repo.ID {
		return nil, common.ErrBad("目录里的仓库 ID 是 %s，与填写的 %s 不一致", id, repo.ID)
	}
	out := make([]CatalogItem, 0, len(doc.Plugins))
	for _, p := range doc.Plugins {
		raw, err := json.Marshal(p.Manifest)
		if err != nil {
			if official {
				return nil, err
			}
			continue
		}
		m, err := sdk.ParseManifest(raw)
		if err != nil {
			if official {
				return nil, err
			}
			continue
		}
		arts := make([]Artifact, 0, len(p.Artifacts))
		for _, a := range p.Artifacts {
			if kept, ok := keepArtifact(a, repo.KeyID); ok {
				arts = append(arts, kept)
			}
		}
		if !official && len(arts) == 0 {
			continue
		}
		out = append(out, CatalogItem{
			Name: m.Name, Official: official, Origin: repo.Name, RepoID: repo.ID,
			Artifacts: arts, Manifest: m,
		})
	}
	return out, nil
}

func (r *Registry) refreshSources(ctx context.Context) {
	r.mu.Lock()
	repos := append([]repoEntry(nil), r.repos...)
	builtin := map[string]bool{}
	for _, c := range r.catalog {
		if c.Official {
			builtin[c.Name] = true
		}
	}
	r.mu.Unlock()

	remote := make([]CatalogItem, 0)
	problems := map[string]string{}
	seen := map[string]bool{}
	for _, repo := range repos {
		items, err := fetchCatalog(ctx, repo)
		if err != nil {
			problems[repo.ID] = err.Error()
			continue
		}
		for _, it := range items {
			if builtin[it.Name] || seen[it.Name] {
				continue
			}
			seen[it.Name] = true
			it.Official = false
			it.Origin = repo.Name
			it.RepoID = repo.ID
			remote = append(remote, it)
		}
	}
	r.mu.Lock()
	r.remote = remote
	r.repoProblems = problems
	r.mu.Unlock()
}

func fetchCatalog(ctx context.Context, repo repoEntry) ([]CatalogItem, error) {
	u, err := validateFetchURL(repo.URL)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, common.ErrBad("读取目录失败: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, common.ErrBad("目录返回 HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseCatalog(body, repo, false)
}

func (m manualEntry) item() CatalogItem {
	man := sdk.Manifest{
		ManifestVersion: sdk.ManifestV,
		Name:            m.Name,
		Title:           m.Title,
		Version:         m.Version,
		Description:     m.Description,
		Source:          m.Source,
		Engines:         sdk.Engines{HostAPI: sdk.HostAPI},
		Primitives:      m.Primitives,
		Targets:         m.Targets,
		Machines:        m.Machines,
		UI:              m.UI,
	}
	if man.Title == "" {
		man.Title = m.Name
	}
	if man.Primitives == nil {
		man.Primitives = []string{}
	}
	if man.Targets == nil {
		man.Targets = []string{}
	}
	if man.Machines == nil {
		man.Machines = []string{}
	}
	return CatalogItem{
		Name: m.Name, Origin: "手动安装", Manifest: man,
		Artifacts: []Artifact{{
			Target: hubTarget(), URL: m.URL, SHA256: m.SHA256,
			Size: m.Size, SigningKeyID: m.SigningKeyID,
		}},
	}
}

func (r *Registry) manualItems() []CatalogItem {
	out := make([]CatalogItem, 0, len(r.manual))
	for _, m := range r.manual {
		out = append(out, m.item())
	}
	return out
}

func (r *Registry) pinItems() []CatalogItem {
	out := make([]CatalogItem, 0, len(r.pins))
	for _, m := range r.pins {
		out = append(out, CatalogItem{
			Name: m.Name, Origin: "已安装",
			Manifest: sdk.Manifest{Name: m.Name, Title: m.Name, Version: m.Version},
		})
	}
	return out
}

// lookup walks the built-in catalog, manual installs, fetched repositories
// and pins. The caller holds r.mu. Built-in names win, so a custom catalog
// cannot replace an official plugin.
func (r *Registry) lookup(name string) (CatalogItem, bool) {
	for _, list := range [][]CatalogItem{r.catalog, r.manualItems(), r.remote, r.pinItems()} {
		for _, c := range list {
			if c.Name == name {
				if c.Manifest.Name == "" {
					c.Manifest.Name = c.Name
				}
				return c, true
			}
		}
	}
	return CatalogItem{}, false
}

func (r *Registry) addManual(ctx context.Context, name, rawURL, sum, keyID string) error {
	name = strings.TrimSpace(name)
	rawURL = strings.TrimSpace(rawURL)
	keyID = strings.TrimSpace(keyID)
	if !pluginName.MatchString(name) {
		return common.ErrBad("插件名需要是小写字母、数字或短横线")
	}
	if _, err := validateFetchURL(rawURL); err != nil {
		return err
	}
	norm, err := normalizeSHA(sum)
	if err != nil {
		return err
	}
	r.mu.Lock()
	for _, c := range r.catalog {
		if c.Name == name && c.Official {
			r.mu.Unlock()
			return common.ErrBad("插件 %s 已在官方目录中", name)
		}
	}
	pub, ok := r.keys[keyID]
	if !ok {
		r.mu.Unlock()
		return common.ErrBad("签名密钥 %s 还不在信任列表里，先添加带这把公钥的仓库", keyID)
	}
	pub = append([]byte(nil), pub...)
	r.mu.Unlock()

	body, err := downloadPackage(ctx, rawURL, 0)
	if err != nil {
		return err
	}
	if packageSHA(body) != norm {
		return common.ErrBad("安装包 SHA-256 不符")
	}
	pkg, err := sdk.Open(body)
	if err != nil {
		return common.ErrBad("安装包无法打开: %v", err)
	}
	m, err := pkg.Verify(pub, keyID)
	if err != nil {
		return common.ErrBad("安装包签名校验失败")
	}
	if m.Name != name {
		return common.ErrBad("安装包里的插件名是 %s", m.Name)
	}
	entry := manualEntry{
		Name: name, URL: rawURL, SHA256: norm, SigningKeyID: keyID, Size: int64(len(body)),
		Title: m.Title, Version: m.Version, Description: m.Description, Source: m.Source,
		Primitives: m.Primitives, Targets: m.Targets, Machines: m.Machines, UI: m.UI,
	}
	r.mu.Lock()
	next := make([]manualEntry, 0, len(r.manual)+1)
	replaced := false
	for _, old := range r.manual {
		if old.Name == name {
			next = append(next, entry)
			replaced = true
			continue
		}
		next = append(next, old)
	}
	if !replaced {
		next = append(next, entry)
	}
	r.manual = next
	err = r.saveLocked()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.Install(ctx, name)
}

func (r *Registry) removeManual(name string) error {
	if !pluginName.MatchString(name) {
		return errNotFound(name)
	}
	r.mu.Lock()
	found := false
	var keyID string
	next := r.manual[:0:0]
	for _, m := range r.manual {
		if m.Name == name {
			found = true
			keyID = m.SigningKeyID
			continue
		}
		next = append(next, m)
	}
	if !found {
		r.mu.Unlock()
		return errNotFound(name)
	}
	wanted := contains(r.wanted, name)
	r.manual = next
	r.forgetKey(keyID)
	if err := r.saveLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	if wanted {
		return r.Uninstall(name)
	}
	return nil
}

func (r *Registry) addRepo(ctx context.Context, id, name, rawURL, keyID, publicKey string) error {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	rawURL = strings.TrimSpace(rawURL)
	keyID = strings.TrimSpace(keyID)
	publicKey = strings.TrimSpace(publicKey)
	if !pluginName.MatchString(id) || id == "official" || id == "vartio" {
		return common.ErrBad("仓库 ID 需要是小写字母、数字或短横线")
	}
	if name == "" || len([]rune(name)) > 40 {
		return common.ErrBad("请填写仓库名称")
	}
	if _, err := validateFetchURL(rawURL); err != nil {
		return err
	}
	pub, err := sdk.ParsePublicKey(publicKey)
	if err != nil || keyID == "" {
		return common.ErrBad("请填写这把仓库的密钥 ID 和公钥")
	}
	r.mu.Lock()
	for _, c := range r.repos {
		if c.ID == id {
			r.mu.Unlock()
			return common.ErrBad("仓库 %s 已存在", id)
		}
	}
	if err := r.trust(keyID, pub); err != nil {
		r.mu.Unlock()
		return err
	}
	r.repos = append(r.repos, repoEntry{ID: id, Name: name, URL: rawURL, KeyID: keyID, PublicKey: publicKey})
	err = r.saveLocked()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.refreshSources(ctx)
	return nil
}

func (r *Registry) removeRepo(ctx context.Context, id string) error {
	r.mu.Lock()
	next := r.repos[:0:0]
	found := false
	var keyID string
	for _, c := range r.repos {
		if c.ID == id {
			found = true
			keyID = c.KeyID
			continue
		}
		next = append(next, c)
	}
	if !found {
		r.mu.Unlock()
		return errNotFound(id)
	}
	r.repos = next
	r.forgetKey(keyID)
	err := r.saveLocked()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.refreshSources(ctx)
	return nil
}

func (r *Registry) upsertPin(item CatalogItem, art Artifact) {
	next := make([]pinEntry, 0, len(r.pins)+1)
	for _, p := range r.pins {
		if p.Name == item.Name {
			continue
		}
		next = append(next, p)
	}
	next = append(next, pinEntry{
		Name: item.Name, Version: item.Version, Target: art.Target,
		URL: art.URL, SHA256: art.SHA256, SigningKeyID: art.SigningKeyID, Size: art.Size,
	})
	r.pins = next
}

func (r *Registry) dropPin(name string) {
	next := r.pins[:0:0]
	for _, p := range r.pins {
		if p.Name != name {
			next = append(next, p)
		}
	}
	r.pins = next
}

type sourceView struct {
	Official struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Builtin bool   `json:"builtin"`
		Enabled bool   `json:"enabled"`
	} `json:"official"`
	Repos  []repoView    `json:"repos"`
	Manual []manualEntry `json:"manual"`
}

type repoView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	KeyID   string `json:"keyId"`
	Plugins int    `json:"plugins"`
	Error   string `json:"error,omitempty"`
}

func (r *Registry) sourceView() sourceView {
	r.mu.Lock()
	defer r.mu.Unlock()
	var v sourceView
	v.Official.ID = "vartio"
	v.Official.Name = "Vartio 官方"
	v.Official.Builtin = true
	v.Official.Enabled = true
	v.Manual = append([]manualEntry(nil), r.manual...)
	if v.Manual == nil {
		v.Manual = []manualEntry{}
	}
	v.Repos = make([]repoView, 0, len(r.repos))
	for _, repo := range r.repos {
		n := 0
		for _, it := range r.remote {
			if it.RepoID == repo.ID {
				n++
			}
		}
		v.Repos = append(v.Repos, repoView{
			ID: repo.ID, Name: repo.Name, URL: repo.URL, KeyID: repo.KeyID,
			Plugins: n, Error: r.repoProblems[repo.ID],
		})
	}
	return v
}

func (r *Registry) handleSources(w http.ResponseWriter, req *http.Request) {
	r.refreshSources(req.Context())
	writeJSON(w, http.StatusOK, r.sourceView())
}

func (r *Registry) handleAddRepo(w http.ResponseWriter, req *http.Request) {
	var body repoEntry
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		WriteError(w, common.ErrBad("invalid json"))
		return
	}
	if err := r.addRepo(req.Context(), body.ID, body.Name, body.URL, body.KeyID, body.PublicKey); err != nil {
		WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, r.sourceView())
}

func (r *Registry) handleDelRepo(w http.ResponseWriter, req *http.Request) {
	if err := r.removeRepo(req.Context(), req.PathValue("id")); err != nil {
		WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, r.sourceView())
}

func (r *Registry) handleAddManual(w http.ResponseWriter, req *http.Request) {
	var body manualEntry
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		WriteError(w, common.ErrBad("invalid json"))
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Minute)
	defer cancel()
	if err := r.addManual(ctx, body.Name, body.URL, body.SHA256, body.SigningKeyID); err != nil {
		WriteError(w, err)
		return
	}
	it, _ := r.item(strings.TrimSpace(body.Name), r.sessions())
	writeJSON(w, http.StatusOK, it)
}

func (r *Registry) handleDelManual(w http.ResponseWriter, req *http.Request) {
	if err := r.removeManual(req.PathValue("name")); err != nil {
		WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
