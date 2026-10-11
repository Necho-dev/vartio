package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"vartio/internal/common"
)

// A Vartio catalog is a JSON document, the same shape DBX publishes as
// catalogVersion 1, but each plugin points at a Git repository instead of a
// signed package. The hub clones that repository and builds it locally.
// DBX additionally pins a sha256 and an Ed25519 signing key per artifact;
// this hub does not verify a package signature.

const catalogVersion = 1

var (
	gitURL = regexp.MustCompile(`^(git@[a-zA-Z0-9.-]+:[a-zA-Z0-9_./-]+(\.git)?|https://[a-zA-Z0-9.-]+/[a-zA-Z0-9_./-]+(\.git)?)$`)
	repoID = pluginName
)

type gitEntry struct {
	Name string `json:"name"`
	Git  string `json:"git"`
}

type repoEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
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
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Git         string   `json:"git"`
	Primitives  []string `json:"primitives"`
	Targets     []string `json:"targets"`
}

func validateGit(raw string) error {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "..") || !gitURL.MatchString(raw) {
		return common.ErrBad("Git 地址只接受 git@主机:路径 或 https://主机/路径")
	}
	return nil
}

func validateCatalogURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return nil, common.ErrBad("目录地址需要是 http 或 https URL")
	}
	return u, nil
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
	u, err := validateCatalogURL(repo.URL)
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
	var doc catalogDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, common.ErrBad("目录不是 JSON")
	}
	if doc.CatalogVersion != catalogVersion {
		return nil, common.ErrBad("目录版本需要是 %d", catalogVersion)
	}
	if id := strings.TrimSpace(doc.Repository.ID); id != "" && id != repo.ID {
		return nil, common.ErrBad("目录里的仓库 ID 是 %s，与填写的 %s 不一致", id, repo.ID)
	}
	out := make([]CatalogItem, 0, len(doc.Plugins))
	for _, p := range doc.Plugins {
		p.Name = strings.TrimSpace(p.Name)
		p.Git = strings.TrimSpace(p.Git)
		if !pluginName.MatchString(p.Name) || validateGit(p.Git) != nil {
			continue
		}
		title := strings.TrimSpace(p.Title)
		if title == "" {
			title = p.Name
		}
		out = append(out, CatalogItem{
			Name: p.Name, Git: p.Git, Origin: repo.Name,
			Manifest: Manifest{
				Name: p.Name, Title: title, Version: strings.TrimSpace(p.Version),
				Description: p.Description, Primitives: p.Primitives, Targets: p.Targets,
			},
		})
	}
	return out, nil
}

func (r *Registry) manualItems() []CatalogItem {
	out := make([]CatalogItem, 0, len(r.manual))
	for _, m := range r.manual {
		out = append(out, CatalogItem{
			Name: m.Name, Git: m.Git, Origin: "手动安装",
			Manifest: Manifest{Name: m.Name, Title: m.Name},
		})
	}
	return out
}

func (r *Registry) pinItems() []CatalogItem {
	out := make([]CatalogItem, 0, len(r.pins))
	for _, m := range r.pins {
		out = append(out, CatalogItem{
			Name: m.Name, Git: m.Git, Origin: "已安装",
			Manifest: Manifest{Name: m.Name, Title: m.Name},
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

func (r *Registry) addManual(ctx context.Context, name, git string) error {
	name, git = strings.TrimSpace(name), strings.TrimSpace(git)
	if !pluginName.MatchString(name) {
		return common.ErrBad("插件名需要是小写字母、数字或短横线")
	}
	if err := validateGit(git); err != nil {
		return err
	}
	r.mu.Lock()
	for _, c := range r.catalog {
		if c.Name == name && c.Official {
			r.mu.Unlock()
			return common.ErrBad("插件 %s 已在官方目录中", name)
		}
	}
	next := make([]gitEntry, 0, len(r.manual)+1)
	replaced := false
	for _, m := range r.manual {
		if m.Name == name {
			next = append(next, gitEntry{Name: name, Git: git})
			replaced = true
			continue
		}
		next = append(next, m)
	}
	if !replaced {
		next = append(next, gitEntry{Name: name, Git: git})
	}
	r.manual = next
	err := r.saveLocked()
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
	next := r.manual[:0:0]
	for _, m := range r.manual {
		if m.Name == name {
			found = true
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

func (r *Registry) addRepo(ctx context.Context, id, name, rawURL string) error {
	id, name, rawURL = strings.TrimSpace(id), strings.TrimSpace(name), strings.TrimSpace(rawURL)
	if !repoID.MatchString(id) || id == "official" || id == "vartio" {
		return common.ErrBad("仓库 ID 需要是小写字母、数字或短横线")
	}
	if name == "" || len([]rune(name)) > 40 {
		return common.ErrBad("请填写仓库名称")
	}
	if _, err := validateCatalogURL(rawURL); err != nil {
		return err
	}
	r.mu.Lock()
	for _, c := range r.repos {
		if c.ID == id {
			r.mu.Unlock()
			return common.ErrBad("仓库 %s 已存在", id)
		}
	}
	r.repos = append(r.repos, repoEntry{ID: id, Name: name, URL: rawURL})
	err := r.saveLocked()
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
	for _, c := range r.repos {
		if c.ID == id {
			found = true
			continue
		}
		next = append(next, c)
	}
	if !found {
		r.mu.Unlock()
		return errNotFound(id)
	}
	r.repos = next
	err := r.saveLocked()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.refreshSources(ctx)
	return nil
}

func (r *Registry) upsertPin(item CatalogItem) {
	if item.Git == "" {
		return
	}
	next := make([]gitEntry, 0, len(r.pins)+1)
	for _, p := range r.pins {
		if p.Name == item.Name {
			continue
		}
		next = append(next, p)
	}
	next = append(next, gitEntry{Name: item.Name, Git: item.Git})
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
	Repos  []repoView `json:"repos"`
	Manual []gitEntry `json:"manual"`
}

type repoView struct {
	repoEntry
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
	v.Manual = append([]gitEntry(nil), r.manual...)
	if v.Manual == nil {
		v.Manual = []gitEntry{}
	}
	v.Repos = make([]repoView, 0, len(r.repos))
	for _, repo := range r.repos {
		n := 0
		for _, it := range r.remote {
			if it.RepoID == repo.ID {
				n++
			}
		}
		v.Repos = append(v.Repos, repoView{repoEntry: repo, Plugins: n, Error: r.repoProblems[repo.ID]})
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
	if err := r.addRepo(req.Context(), body.ID, body.Name, body.URL); err != nil {
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
	var body gitEntry
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		WriteError(w, common.ErrBad("invalid json"))
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Minute)
	defer cancel()
	if err := r.addManual(ctx, body.Name, body.Git); err != nil {
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
