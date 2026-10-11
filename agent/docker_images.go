package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Image update hints: a container's image is out of date when the registry's digest
// for its tag is none of the local image's RepoDigests. Checked in the background,
// one image at a time, at most every imageCheckEvery; HEAD requests do not count
// against Docker Hub pull limits. Private registries that need credentials are
// skipped. GUEST_DOCKER_IMAGE_CHECK=off disables the check.

const imageCheckEvery = 6 * time.Hour

var (
	registryScheme = "https"
	registryClient = &http.Client{Timeout: 15 * time.Second}
)

type imageUpdates struct {
	mu      sync.Mutex
	running bool
	status  map[string]*imageStatus // imageKey
}

type imageStatus struct {
	update  bool
	checked time.Time
}

func imageCheckOff() bool {
	return strings.EqualFold(strings.TrimSpace(getenv("VARTIO_AGENT_DOCKER_IMAGE_CHECK", "GUEST_DOCKER_IMAGE_CHECK")), "off")
}

// imageKey ties the verdict to the tag and the image the container actually runs:
// pulling a newer image does not clear the hint until the container is recreated.
func imageKey(c apiContainer) string {
	if _, _, _, ok := parseImageRef(c.Image); !ok || c.ImageID == "" {
		return ""
	}
	return c.Image + "|" + c.ImageID
}

func (u *imageUpdates) available(c apiContainer) bool {
	k := imageKey(c)
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.status[k]
	return k != "" && st != nil && st.update
}

// refresh starts one background pass over images not checked recently.
func (u *imageUpdates) refresh(e *dockerEngine, cs []apiContainer) {
	if imageCheckOff() {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return
	}
	if u.status == nil {
		u.status = map[string]*imageStatus{}
	}
	active := map[string]bool{}
	var due []apiContainer
	for _, c := range cs {
		k := imageKey(c)
		if k == "" || active[k] {
			continue
		}
		active[k] = true
		if st := u.status[k]; st == nil || time.Since(st.checked) > imageCheckEvery {
			due = append(due, c)
		}
	}
	for k := range u.status {
		if !active[k] {
			delete(u.status, k)
		}
	}
	if len(due) == 0 {
		return
	}
	u.running = true
	go func() {
		for _, c := range due {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			update, err := e.imageOutdated(ctx, c)
			cancel()
			u.mu.Lock()
			st := &imageStatus{update: update, checked: time.Now()}
			if err != nil {
				// Retry an unreachable registry sooner than a successful check.
				st.checked = time.Now().Add(-imageCheckEvery + 30*time.Minute)
			}
			u.status[imageKey(c)] = st
			u.mu.Unlock()
		}
		u.mu.Lock()
		u.running = false
		u.mu.Unlock()
	}()
}

func (e *dockerEngine) imageOutdated(ctx context.Context, c apiContainer) (bool, error) {
	var img struct{ RepoDigests []string }
	if err := e.call(ctx, http.MethodGet, "/images/"+url.PathEscape(c.ImageID)+"/json", nil, &img); err != nil {
		return false, err
	}
	if len(img.RepoDigests) == 0 {
		return false, nil // built locally: nothing to compare with
	}
	reg, repo, tag, _ := parseImageRef(c.Image)
	remote, err := registryDigest(ctx, reg, repo, tag)
	if err != nil || remote == "" {
		return false, err
	}
	for _, d := range img.RepoDigests {
		if _, digest, ok := strings.Cut(d, "@"); ok && digest == remote {
			return false, nil
		}
	}
	return true, nil
}

// parseImageRef splits "nginx", "ghcr.io/o/app:1", "localhost:5000/x:dev". Digest-pinned
// references and bare image ids cannot move, so they are not checked.
func parseImageRef(image string) (registry, repo, tag string, ok bool) {
	image = strings.TrimSpace(image)
	if image == "" || strings.Contains(image, "@") || strings.HasPrefix(image, "sha256:") {
		return "", "", "", false
	}
	name, tag := image, "latest"
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	}
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first, rest, tag, rest != ""
	}
	if !found {
		name = "library/" + name
	}
	return "docker.io", name, tag, true
}

var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}, ", ")

// registryDigest HEADs the tag's manifest, answering a Bearer challenge with an
// anonymous token.
func registryDigest(ctx context.Context, registry, repo, tag string) (string, error) {
	host := registry
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	target := registryScheme + "://" + host + "/v2/" + repo + "/manifests/" + url.PathEscape(tag)
	head := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", manifestAccept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return registryClient.Do(req)
	}
	resp, err := head("")
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		token, err := anonymousToken(ctx, resp.Header.Get("WWW-Authenticate"), repo)
		if err != nil {
			return "", err
		}
		if resp, err = head(token); err != nil {
			return "", err
		}
		resp.Body.Close()
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry %s: %s", host, resp.Status)
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}

func anonymousToken(ctx context.Context, challenge, repo string) (string, error) {
	params := parseChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry wants credentials (%q)", challenge)
	}
	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := registryClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token: %s", resp.Status)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.Token != "" {
		return t.Token, nil
	}
	return t.AccessToken, nil
}

// parseChallenge reads `Bearer realm="…",service="…",scope="…"`.
func parseChallenge(h string) map[string]string {
	out := map[string]string{}
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return out
	}
	for rest != "" {
		var kv string
		// Values are quoted and may contain commas (scope lists).
		k, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, ","))
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				break
			}
			kv, rest = after[1:end+1], after[end+2:]
		} else {
			kv, rest, _ = strings.Cut(after, ",")
		}
		out[strings.ToLower(k)] = kv
	}
	return out
}
