package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"govmomi-wmks/internal/guest"
)

// Docker Engine API over the engine's socket. Podman, OrbStack, colima and
// Rancher Desktop serve the same API, so one client covers them all.

const (
	dockerOff = "off"
	dockerRO  = "ro"
	dockerRW  = "rw"
)

// dockerMode is GUEST_DOCKER: off, ro (default: containers, stats and logs) or
// rw (adds a shell inside containers). Socket access is root-equivalent on the
// host, so the shell is opt-in.
func dockerMode() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("GUEST_DOCKER"))); v {
	case dockerOff, dockerRW:
		return v
	}
	return dockerRO
}

const (
	dockerStatsWorkers = 8
	dockerStatsMax     = 100
	dockerLogTail      = 500
	dockerLogTailMax   = 5000
)

type dockerEngine struct {
	network, addr string
	hc            *http.Client

	mu   sync.Mutex
	info engineInfo
	idAt time.Time

	images imageUpdates
}

// engineInfo decides how stats are read: Podman's system_cpu_usage comes from the
// container's cgroup rather than the host, Windows counts CPU in 100ns units, and
// Docker before 25 ignores one-shot, so every stats call waits a full second.
type engineInfo struct {
	name, ver string
	podman    bool
	windows   bool
	major     int
}

var engines sync.Map

// docker returns the engine to talk to, or nil when none is configured or found.
func docker() *dockerEngine {
	if dockerMode() == dockerOff {
		return nil
	}
	network, addr := dockerAddr()
	if addr == "" {
		return nil
	}
	key := network + "://" + addr
	if e, ok := engines.Load(key); ok {
		return e.(*dockerEngine)
	}
	e, _ := engines.LoadOrStore(key, newEngine(network, addr))
	return e.(*dockerEngine)
}

// dockerAddr honours DOCKER_HOST (unix:// or tcp://, no TLS), then probes the
// usual socket locations.
func dockerAddr() (network, addr string) {
	if h := strings.TrimSpace(os.Getenv("DOCKER_HOST")); h != "" {
		u, err := url.Parse(h)
		if err != nil {
			return "", ""
		}
		switch u.Scheme {
		case "unix":
			return "unix", u.Path
		case "tcp", "http":
			return "tcp", u.Host
		}
		return "", ""
	}
	for _, p := range dockerSockets() {
		if st, err := os.Stat(p); err == nil && st.Mode()&os.ModeSocket != 0 {
			return "unix", p
		}
	}
	return "", ""
}

func dockerSockets() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	list := []string{"/var/run/docker.sock", "/run/podman/podman.sock"}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		list = append(list, filepath.Join(xdg, "docker.sock"), filepath.Join(xdg, "podman", "podman.sock"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, p := range []string{".docker/run/docker.sock", ".orbstack/run/docker.sock", ".colima/default/docker.sock", ".rd/docker.sock"} {
			list = append(list, filepath.Join(home, p))
		}
	}
	return list
}

func newEngine(network, addr string) *dockerEngine {
	d := &net.Dialer{Timeout: 3 * time.Second}
	e := &dockerEngine{network: network, addr: addr}
	e.hc = &http.Client{Transport: &http.Transport{
		DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, network, addr) },
		MaxIdleConnsPerHost: dockerStatsWorkers,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}}
	return e
}

func (e *dockerEngine) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, engineErr(resp)
	}
	return resp, nil
}

func (e *dockerEngine) call(ctx context.Context, method, path string, body, out any) error {
	resp, err := e.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func engineErr(resp *http.Response) error {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&m)
	if m.Message == "" {
		m.Message = resp.Status
	}
	code := "docker_error"
	switch resp.StatusCode {
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusConflict:
		code = "conflict"
	}
	return &guest.FrameError{Code: code, Message: m.Message}
}

// identity is the engine product, version and quirks, refreshed every 10 minutes.
func (e *dockerEngine) identity(ctx context.Context) engineInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.info.ver != "" && time.Since(e.idAt) < 10*time.Minute {
		return e.info
	}
	resp, err := e.do(ctx, http.MethodGet, "/version", nil)
	if err != nil {
		return e.info
	}
	defer resp.Body.Close()
	var v struct {
		Platform   struct{ Name string }
		Version    string
		Os         string
		Components []struct{ Name string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return e.info
	}
	e.info, e.idAt = parseEngine(resp.Header.Get("Server"), v.Platform.Name, v.Version, v.Os, v.Components), time.Now()
	return e.info
}

func parseEngine(server, platform, version, goos string, components []struct{ Name string }) engineInfo {
	info := engineInfo{name: platform, ver: version,
		podman:  strings.HasPrefix(server, "Libpod"),
		windows: strings.EqualFold(goos, "windows") || strings.Contains(strings.ToLower(server), "windows")}
	for _, c := range components {
		if strings.HasPrefix(c.Name, "Podman") {
			info.podman = true
		}
	}
	if (info.name == "" || strings.Contains(info.name, "/")) && len(components) > 0 {
		info.name = components[0].Name
	}
	if info.name == "" {
		info.name = "Docker Engine"
	}
	if !info.podman {
		_, _ = fmt.Sscanf(version, "%d", &info.major)
	}
	return info
}

type apiContainer struct {
	ID      string `json:"Id"`
	Names   []string
	Image   string
	ImageID string
	State   string
	Status  string
	Created int64
	Labels  map[string]string
	Ports   []apiPort
	// Health is in the list from API 1.52; older engines only put it in Status.
	Health struct{ Status string }
}

type apiPort struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}

// dockerExcluded matches GUEST_DOCKER_EXCLUDE: comma-separated name globs
// ("buildx_*,*-tmp"). Excluded containers are not listed, sampled, tailed or entered.
func dockerExcluded(name string) bool {
	name = strings.TrimPrefix(name, "/")
	for _, p := range splitCSV(os.Getenv("GUEST_DOCKER_EXCLUDE")) {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// dockerMetrics lists every container (running first) with stats for the running ones.
func dockerMetrics(ctx context.Context) *guest.DockerInfo {
	e := docker()
	if e == nil {
		return nil
	}
	out := &guest.DockerInfo{Containers: []guest.ContainerStat{}}
	id := e.identity(ctx)
	out.Engine, out.Version = id.name, id.ver
	var list []apiContainer
	if err := e.call(ctx, http.MethodGet, "/containers/json?all=1", nil, &list); err != nil {
		out.Error = err.Error()
		return out
	}
	visible := list[:0]
	for _, c := range list {
		if len(c.Names) > 0 && dockerExcluded(c.Names[0]) {
			continue
		}
		visible = append(visible, c)
	}
	e.images.refresh(e, visible)
	out.Total = len(visible)
	for _, c := range visible {
		s := containerStat(c)
		s.Update = e.images.available(c)
		out.Containers = append(out.Containers, s)
		if c.State == "running" {
			out.Running++
		}
	}
	sort.SliceStable(out.Containers, func(i, j int) bool {
		a, b := out.Containers[i], out.Containers[j]
		if (a.State == "running") != (b.State == "running") {
			return a.State == "running"
		}
		return a.Name < b.Name
	})
	if len(out.Containers) > guest.MaxContainers {
		out.Containers = out.Containers[:guest.MaxContainers]
	}
	out.Images = e.listImages(ctx, visible)
	var running []*guest.ContainerStat
	for i := range out.Containers[:min(len(out.Containers), out.Running, dockerStatsMax)] {
		running = append(running, &out.Containers[i])
	}
	// Docker < 25 waits a second per call whatever one-shot says: run them all at once.
	workers := dockerStatsWorkers
	if !id.podman && id.major > 0 && id.major < 25 {
		workers = len(running)
	}
	failed := e.sampleAll(ctx, running, workers, id)
	// A stats call occasionally fails while others are in flight (docker 24); retry once.
	if len(failed) > 0 && ctx.Err() == nil {
		e.sampleAll(ctx, failed, workers, id)
	}
	return out
}

func (e *dockerEngine) sampleAll(ctx context.Context, cs []*guest.ContainerStat, workers int, id engineInfo) []*guest.ContainerStat {
	sem := make(chan struct{}, max(workers, 1))
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed []*guest.ContainerStat
	)
	for _, c := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := e.stats(ctx, c, id); err != nil {
				mu.Lock()
				failed = append(failed, c)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failed
}

func containerStat(c apiContainer) guest.ContainerStat {
	s := guest.ContainerStat{ID: shortID(c.ID), Image: c.Image, State: c.State, Status: c.Status, Created: c.Created,
		Project: c.Labels["com.docker.compose.project"], Service: c.Labels["com.docker.compose.service"],
		Compose: firstComposeFile(c.Labels["com.docker.compose.project.config_files"]),
		Health:  healthOf(c.Status), Ports: formatPorts(c.Ports)}
	if h := normHealth(c.Health.Status); h != "" {
		s.Health = h
	}
	if len(c.Names) > 0 {
		s.Name = strings.TrimPrefix(c.Names[0], "/")
	}
	if strings.HasPrefix(s.Image, "sha256:") {
		s.Image = shortID(strings.TrimPrefix(s.Image, "sha256:"))
	}
	return s
}

func normHealth(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "healthy", "unhealthy", "starting":
		return s
	}
	return ""
}

// formatPorts lists published ports once each (IPv4 and IPv6 wildcards collapse).
func formatPorts(ps []apiPort) string {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].PublicPort != ps[j].PublicPort {
			return ps[i].PublicPort < ps[j].PublicPort
		}
		return ps[i].IP < ps[j].IP
	})
	var out []string
	seen := map[string]bool{}
	for _, p := range ps {
		if p.PublicPort == 0 {
			continue
		}
		s := fmt.Sprintf("%d→%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		switch {
		case p.IP == "" || p.IP == "0.0.0.0" || p.IP == "::":
		case strings.Contains(p.IP, ":"):
			s = "[" + p.IP + "]:" + s
		default:
			s = p.IP + ":" + s
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// healthOf reads the health check state from the status line, e.g. "Up 2 hours (healthy)".
func healthOf(status string) string {
	switch {
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

type apiStats struct {
	CPU struct {
		Usage struct {
			Total  uint64   `json:"total_usage"`
			PerCPU []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
		Online int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	Mem struct {
		Usage   uint64             `json:"usage"`
		Limit   uint64             `json:"limit"`
		Stats   map[string]float64 `json:"stats"`
		Private uint64             `json:"privateworkingset"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		Rx uint64 `json:"rx_bytes"`
		Tx uint64 `json:"tx_bytes"`
	} `json:"networks"`
	Blk struct {
		IO []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	Pids struct {
		Current int `json:"current"`
	} `json:"pids_stats"`
	Read     time.Time `json:"read"`
	NumProcs int       `json:"num_procs"`
}

// stats takes one sample without the engine's built-in one-second wait; the
// hub turns consecutive samples into rates.
func (e *dockerEngine) stats(ctx context.Context, c *guest.ContainerStat, id engineInfo) error {
	var st apiStats
	if err := e.call(ctx, http.MethodGet, "/containers/"+c.ID+"/stats?stream=false&one-shot=true", nil, &st); err != nil {
		return err
	}
	applyStats(c, st, id)
	if id.podman && c.Health == "" {
		c.Health = e.inspectHealth(ctx, c.ID)
	}
	return nil
}

// inspectHealth: Podman leaves health out of the container list.
func (e *dockerEngine) inspectHealth(ctx context.Context, id string) string {
	var info struct {
		State struct {
			Health      struct{ Status string }
			Healthcheck struct{ Status string }
		}
	}
	if e.call(ctx, http.MethodGet, containerPath(id)+"/json", nil, &info) != nil {
		return ""
	}
	if h := normHealth(info.State.Health.Status); h != "" {
		return h
	}
	return normHealth(info.State.Healthcheck.Status)
}

func applyStats(c *guest.ContainerStat, st apiStats, id engineInfo) {
	c.Stats = true
	c.CPUNs, c.SysNs = st.CPU.Usage.Total, st.CPU.System
	c.CPUs = st.CPU.Online
	if c.CPUs == 0 {
		c.CPUs = len(st.CPU.Usage.PerCPU)
	}
	if !st.Read.IsZero() && st.Read.Year() > 1 {
		c.ReadNs = st.Read.UnixNano()
	}
	switch {
	case id.windows:
		c.CPUNs, c.SysNs = st.CPU.Usage.Total*100, 0
		if c.CPUs == 0 {
			c.CPUs = st.NumProcs
		}
	case id.podman:
		c.SysNs = 0
	}
	if c.CPUs == 0 {
		c.CPUs = runtime.NumCPU()
	}
	// Like `docker stats`: page cache the kernel can drop does not count.
	c.MemUsed, c.MemLimit = st.Mem.Usage, st.Mem.Limit
	if id.windows && st.Mem.Private > 0 {
		c.MemUsed = st.Mem.Private
	}
	for _, k := range []string{"inactive_file", "total_inactive_file", "cache"} {
		if v, ok := st.Mem.Stats[k]; ok && uint64(v) < c.MemUsed {
			c.MemUsed -= uint64(v)
			break
		}
	}
	if c.MemUsed == 0 && st.Mem.Private > 0 {
		c.MemUsed = st.Mem.Private
	}
	for _, n := range st.Networks {
		c.NetRx += n.Rx
		c.NetTx += n.Tx
	}
	for _, b := range st.Blk.IO {
		switch strings.ToLower(b.Op) {
		case "read":
			c.BlkRead += b.Value
		case "write":
			c.BlkWrite += b.Value
		}
	}
	c.Pids = st.Pids.Current
}

var errNoDocker = &guest.FrameError{Code: "not_found", Message: "未找到 Docker 引擎（检查 docker.sock 或 DOCKER_HOST）"}

func containerPath(name string) string { return "/containers/" + url.PathEscape(name) }

type containerBrief struct {
	Name   string
	Config struct{ Tty bool }
}

var errExcluded = &guest.FrameError{Code: "agent_policy", Message: "该容器已被探针排除（GUEST_DOCKER_EXCLUDE）"}

// inspectAllowed resolves a container name or id, refusing excluded ones.
func (e *dockerEngine) inspectAllowed(ctx context.Context, name string) (containerBrief, error) {
	var info containerBrief
	if err := e.call(ctx, http.MethodGet, containerPath(name)+"/json", nil, &info); err != nil {
		return info, err
	}
	if dockerExcluded(info.Name) {
		return info, errExcluded
	}
	return info, nil
}

// dockerInspect returns the engine's inspect document without Config.Env,
// which commonly carries credentials.
func dockerInspect(ctx context.Context, name string) (map[string]any, error) {
	e := docker()
	if e == nil {
		return nil, errNoDocker
	}
	resp, err := e.do(ctx, http.MethodGet, containerPath(name)+"/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return sanitizeInspect(io.LimitReader(resp.Body, 4<<20))
}

func sanitizeInspect(r io.Reader) (map[string]any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("inspect: %w", err)
	}
	if n, _ := doc["Name"].(string); dockerExcluded(n) {
		return nil, errExcluded
	}
	if c, ok := doc["Config"].(map[string]any); ok {
		delete(c, "Env")
	}
	return doc, nil
}

// startDockerLogs follows a container's output; input is ignored.
func startDockerLogs(req guest.TermOpenRequest, onOut func([]byte), onExit func()) (io.WriteCloser, func(), func(int, int), error) {
	e := docker()
	if e == nil {
		return nil, nil, nil, errNoDocker
	}
	ctx, cancel := context.WithCancel(context.Background())
	ic, icancel := context.WithTimeout(ctx, 10*time.Second)
	info, err := e.inspectAllowed(ic, req.Container)
	icancel()
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	tail := req.Tail
	if tail <= 0 {
		tail = dockerLogTail
	}
	tail = min(tail, dockerLogTailMax)
	q := fmt.Sprintf("%s/logs?follow=1&stdout=1&stderr=1&timestamps=1&tail=%d", containerPath(req.Container), tail)
	if req.Since > 0 {
		q += fmt.Sprintf("&since=%d", req.Since)
	}
	resp, err := e.do(ctx, http.MethodGet, q, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	go func() {
		defer onExit()
		defer resp.Body.Close()
		w := &crlfWriter{out: onOut}
		if info.Config.Tty {
			_, _ = io.Copy(w, resp.Body)
		} else {
			_ = demuxLogs(resp.Body, w)
		}
	}()
	return discard{}, cancel, func(int, int) {}, nil
}

// demuxLogs strips the 8-byte stream headers of a non-TTY container's log stream.
func demuxLogs(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 32<<10)
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		if _, err := io.CopyN(w, br, n); err != nil {
			return err
		}
	}
}

// crlfWriter turns bare LF into CRLF for the browser terminal.
type crlfWriter struct {
	out  func([]byte)
	last byte
}

func (c *crlfWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	buf := make([]byte, 0, len(p)+bytes.Count(p, []byte{'\n'}))
	for _, b := range p {
		if b == '\n' && c.last != '\r' {
			buf = append(buf, '\r')
		}
		buf = append(buf, b)
		c.last = b
	}
	c.out(buf)
	return len(p), nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
func (discard) Close() error                { return nil }

const execShell = "if command -v bash >/dev/null 2>&1; then exec bash; fi; exec sh"

// execReap hangs up, then kills, every process carrying the session marker $0.
// The engine has no way to stop an exec and leaves a TTY shell running after
// its client goes.
const execReap = `m="PROBE_EXEC=$0"; p=""; for d in /proc/[0-9]*; do tr '\0' '\n' < "$d/environ" 2>/dev/null | grep -qx "$m" && p="$p ${d#/proc/}"; done; ` +
	`[ -z "$p" ] && exit 0; kill -HUP $p 2>/dev/null; sleep 1; kill -KILL $p 2>/dev/null; exit 0`

// startDockerExec runs an interactive shell in a container on a hijacked engine connection.
func startDockerExec(req guest.TermOpenRequest, cols, rows int, onOut func([]byte), onExit func()) (io.WriteCloser, func(), func(int, int), error) {
	if dockerMode() != dockerRW {
		return nil, nil, nil, &guest.FrameError{Code: "agent_policy", Message: "探针未开放容器终端（需 GUEST_DOCKER=rw）"}
	}
	e := docker()
	if e == nil {
		return nil, nil, nil, errNoDocker
	}
	cmd := []string{"/bin/sh", "-c", execShell}
	if req.Shell != "" {
		cmd = []string{req.Shell}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := e.inspectAllowed(ctx, req.Container); err != nil {
		return nil, nil, nil, err
	}
	size := []int{rows, cols}
	var created struct {
		ID string `json:"Id"`
	}
	mark, err := randomHex(8)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := e.call(ctx, http.MethodPost, containerPath(req.Container)+"/exec", map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true, "Cmd": cmd,
		"Env": []string{"TERM=xterm-256color", "COLORTERM=truecolor", "PROBE_EXEC=" + mark}, "ConsoleSize": size,
	}, &created); err != nil {
		return nil, nil, nil, err
	}
	conn, err := e.hijack(ctx, "/exec/"+created.ID+"/start", map[string]any{"Detach": false, "Tty": true, "ConsoleSize": size})
	if err != nil {
		return nil, nil, nil, err
	}
	resize := func(c, r int) {
		rc, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		_ = e.call(rc, http.MethodPost, fmt.Sprintf("/exec/%s/resize?h=%d&w=%d", created.ID, r, c), nil, nil)
	}
	resize(cols, rows)
	// One worker applies sizes in order; a newer size replaces one not yet sent.
	sizes, done := make(chan [2]int, 1), make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sizes:
				resize(s[0], s[1])
			case <-done:
				return
			}
		}
	}()
	push := func(c, r int) {
		for {
			select {
			case sizes <- [2]int{c, r}:
				return
			default:
				select {
				case <-sizes:
				default:
				}
			}
		}
	}
	go func() {
		defer close(done)
		defer onExit()
		buf := make([]byte, 16<<10)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				onOut(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
	closer := func() {
		_ = conn.Close()
		go e.reap(req.Container, mark)
	}
	return conn, closer, push, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (e *dockerEngine) reap(container, mark string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var created struct {
		ID string `json:"Id"`
	}
	if err := e.call(ctx, http.MethodPost, containerPath(container)+"/exec", map[string]any{
		"Cmd": []string{"/bin/sh", "-c", execReap, mark},
	}, &created); err != nil {
		return
	}
	_ = e.call(ctx, http.MethodPost, "/exec/"+created.ID+"/start", map[string]any{"Detach": true}, nil)
}

type hijacked struct {
	net.Conn
	r *bufio.Reader
}

func (h *hijacked) Read(p []byte) (int, error) { return h.r.Read(p) }

// hijack POSTs path and returns the raw stream the engine switches to.
func (e *dockerEngine) hijack(ctx context.Context, path string, body any) (*hijacked, error) {
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, e.network, e.addr)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, "http://docker"+path, bytes.NewReader(b))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		err := engineErr(resp)
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &hijacked{Conn: conn, r: br}, nil
}

const (
	maxImages      = 200
	maxComposeFile = 1 << 20
	maxLogBytes    = 2 << 20
)

type apiImage struct {
	ID         string   `json:"Id"`
	RepoTags   []string `json:"RepoTags"`
	Size       int64    `json:"Size"`
	Created    int64    `json:"Created"`
	Containers int      `json:"Containers"`
}

func (e *dockerEngine) listImages(ctx context.Context, cs []apiContainer) []guest.DockerImage {
	var list []apiImage
	if err := e.call(ctx, http.MethodGet, "/images/json", nil, &list); err != nil {
		return nil
	}
	used := map[string]int{}
	for _, c := range cs {
		if c.ImageID != "" {
			used[c.ImageID]++
		}
		if c.Image != "" {
			used[c.Image]++
		}
	}
	out := make([]guest.DockerImage, 0, min(len(list), maxImages))
	for _, img := range list {
		n := imageName(img)
		inUse := img.Containers
		if inUse < 0 {
			inUse = used[img.ID] + used[n]
		}
		out = append(out, guest.DockerImage{ID: shortID(strings.TrimPrefix(img.ID, "sha256:")), Name: n, Tags: namedTags(img.RepoTags),
			Size: img.Size, Created: img.Created, InUse: inUse})
		if len(out) >= maxImages {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].InUse > 0) != (out[j].InUse > 0) {
			return out[i].InUse > 0
		}
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func namedTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if t != "" && t != "<none>:<none>" {
			out = append(out, t)
		}
	}
	return out
}

func imageName(img apiImage) string {
	if tags := namedTags(img.RepoTags); len(tags) > 0 {
		return tags[0]
	}
	return shortID(strings.TrimPrefix(img.ID, "sha256:"))
}

func firstComposeFile(v string) string {
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			return p
		}
	}
	return ""
}

func dockerLogs(ctx context.Context, req guest.DockerLogsRequest) (guest.DockerLogsResult, error) {
	e := docker()
	if e == nil {
		return guest.DockerLogsResult{}, errNoDocker
	}
	info, err := e.inspectAllowed(ctx, req.Container)
	if err != nil {
		return guest.DockerLogsResult{}, err
	}
	tail := req.Tail
	if tail <= 0 {
		tail = dockerLogTail
	}
	tail = min(tail, dockerLogTailMax)
	q := fmt.Sprintf("%s/logs?stdout=1&stderr=1&timestamps=1&tail=%d", containerPath(req.Container), tail)
	if req.Since > 0 {
		q += fmt.Sprintf("&since=%d", req.Since)
	}
	if req.Until > 0 {
		q += fmt.Sprintf("&until=%d", req.Until)
	}
	resp, err := e.do(ctx, http.MethodGet, q, nil)
	if err != nil {
		return guest.DockerLogsResult{}, err
	}
	defer resp.Body.Close()
	text, err := readLogBody(io.LimitReader(resp.Body, maxLogBytes+1), info.Config.Tty)
	if err != nil && err != io.EOF {
		return guest.DockerLogsResult{}, err
	}
	if len(text) > maxLogBytes {
		text = text[len(text)-maxLogBytes:]
	}
	return guest.DockerLogsResult{Text: text}, nil
}

func readLogBody(r io.Reader, tty bool) (string, error) {
	var buf bytes.Buffer
	var err error
	if tty {
		_, err = io.Copy(&buf, r)
	} else {
		err = demuxLogs(r, &buf)
	}
	return buf.String(), err
}

func dockerCompose(ctx context.Context, project string) (guest.DockerComposeResult, error) {
	e := docker()
	if e == nil {
		return guest.DockerComposeResult{}, errNoDocker
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return guest.DockerComposeResult{}, &guest.FrameError{Code: "bad_request", Message: "project required"}
	}
	var list []apiContainer
	if err := e.call(ctx, http.MethodGet, "/containers/json?all=1", nil, &list); err != nil {
		return guest.DockerComposeResult{}, err
	}
	out := guest.DockerComposeResult{Project: project}
	seen := map[string]bool{}
	var files []string
	for _, c := range list {
		if c.Labels["com.docker.compose.project"] != project {
			continue
		}
		if svc := c.Labels["com.docker.compose.service"]; svc != "" && !seen[svc] {
			seen[svc] = true
			out.Services = append(out.Services, svc)
		}
		if f := firstComposeFile(c.Labels["com.docker.compose.project.config_files"]); f != "" {
			files = append(files, f)
		}
	}
	sort.Strings(out.Services)
	if len(files) == 0 {
		out.Error = "容器没有记录编排文件路径"
		return out, nil
	}
	path, body, err := readComposeFile(files)
	out.File = path
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.Content = body
	return out, nil
}

func readComposeFile(files []string) (string, string, error) {
	tried := map[string]bool{}
	var last error
	for _, f := range files {
		for _, p := range composeCandidates(f) {
			if tried[p] {
				continue
			}
			tried[p] = true
			b, err := os.ReadFile(p)
			if err != nil {
				last = err
				continue
			}
			if len(b) > maxComposeFile {
				b = b[:maxComposeFile]
			}
			return f, string(b), nil
		}
	}
	if last == nil {
		last = fmt.Errorf("找不到编排文件")
	}
	return files[0], "", fmt.Errorf("无法读取 %s：%w", files[0], last)
}

func composeCandidates(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(x string) {
		if x == "" || seen[x] {
			return
		}
		seen[x] = true
		out = append(out, x)
	}
	add(p)
	if r := hostRoot(); r != "" {
		add(filepath.Join(r, strings.TrimPrefix(p, string(filepath.Separator))))
	}
	// Docker Desktop labels keep the Mac path (/Users/…); the Linux VM may also
	// expose it under /host_mnt. Do not join hostRoot+"/host_mnt" — that bind hangs.
	if strings.HasPrefix(p, "/Users/") {
		add("/host_mnt" + p)
	}
	return out
}
