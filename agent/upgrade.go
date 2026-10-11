package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vartio/internal/entities"
	"vartio/internal/release"
)

// Upgrade flow: stage (download, checksum, signature, self-test) -> reply ->
// swap the binary and restart -> the new version reports and confirms on its
// first successful hello. A watchdog started from the previous binary rolls
// back if that confirmation does not arrive within the probation window.

const maxArtifact = 256 << 20

var upgrading atomic.Bool

// upgradeState is persisted next to the binary while an upgrade is in flight.
type upgradeState struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Mode    string    `json:"mode"`
	Phase   string    `json:"phase"` // pending, rolled_back
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
	Runner  string    `json:"runner"`
	PID     int       `json:"pid"`
	Exe     string    `json:"exe"`
	Args    []string  `json:"args"`
	Cwd     string    `json:"cwd"`
}

// upgradeEnabled is on unless GUEST_UPGRADE=off. A container keeps the binary
// in its image unless the edge image (or GUEST_UPGRADE=on) opts into replacement.
func upgradeEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(getenv("VARTIO_AGENT_UPGRADE", "GUEST_UPGRADE")))
	if inContainer() {
		return v == "on"
	}
	return v != "off"
}

func probation() time.Duration {
	if d, err := time.ParseDuration(getenv("VARTIO_AGENT_UPGRADE_PROBATION", "GUEST_UPGRADE_PROBATION")); err == nil && d >= 30*time.Second {
		return d
	}
	return 2 * time.Minute
}

func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func statePath(exe string) string { return exe + ".upgrade.json" }

func readState(path string) (*upgradeState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st upgradeState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeState(path string, st *upgradeState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// startupReport looks at a leftover state file: either we are the new version on
// probation, or the watchdog rolled back to us. Anything else is stale.
func startupReport() *entities.UpgradeReport {
	exe, err := selfExe()
	if err != nil {
		return nil
	}
	st, err := readState(statePath(exe))
	if err != nil {
		go removeOld(exe)
		return nil
	}
	switch {
	case st.Phase == "pending" && st.To == agentVersion:
		log.Printf("upgrade: running %s on probation (from %s)", st.To, st.From)
		setLink(func(s *linkState) { s.Probation = st.To })
		return &entities.UpgradeReport{From: st.From, To: st.To, Mode: st.Mode, OK: true}
	case st.Phase == "rolled_back" && st.From == agentVersion:
		log.Printf("upgrade: rolled back from %s: %s", st.To, st.Error)
		return &entities.UpgradeReport{From: st.From, To: st.To, Mode: st.Mode, OK: false, Error: st.Error}
	}
	_ = os.Remove(statePath(exe))
	return nil
}

var report struct {
	once sync.Once
	r    *entities.UpgradeReport
}

func pendingReport() *entities.UpgradeReport {
	report.once.Do(func() { report.r = startupReport() })
	return report.r
}

// confirmUpgrade runs after a hello was accepted: the hub has the report, and a
// new version that got this far is healthy, so the watchdog can stand down.
func confirmUpgrade() {
	if pendingReport() == nil {
		return
	}
	if exe, err := selfExe(); err == nil {
		_ = os.Remove(statePath(exe))
		go removeOld(exe)
	}
	report.r = nil
}

// removeOld deletes the previous build kept for rollback once it is no longer
// needed. On Windows the watchdog runs from it until it sees the confirmation,
// so locked files are retried for a while.
func removeOld(exe string) {
	olds, _ := filepath.Glob(exe + ".old*")
	for i := 0; len(olds) > 0; i++ {
		rest := olds[:0]
		for _, p := range olds {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				rest = append(rest, p)
			}
		}
		if olds = rest; len(olds) == 0 {
			return
		}
		if i == 20 {
			log.Printf("upgrade: could not remove %v", olds)
			return
		}
		time.Sleep(3 * time.Second)
	}
}

func hubBase(control string) (*url.URL, error) {
	u, err := url.Parse(control)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	u.Path, u.RawQuery = "", ""
	return u, nil
}

func (cfg config) hubGet(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	base, err := hubBase(cfg.URL)
	if err != nil {
		return nil, err
	}
	base.Path = path
	base.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	cfg.setAgentAuth(req.Header, loadHubEntry(cfg.URL))
	return http.DefaultClient.Do(req)
}

// upgrade stages req and, if that succeeds, restarts into it shortly after the
// caller has had time to send the reply. step (may be nil) receives progress.
func upgrade(ctx context.Context, cfg config, req entities.UpgradeRequest, step func(entities.UpgradeStep)) entities.UpgradeResult {
	if step == nil {
		step = func(entities.UpgradeStep) {}
	}
	res := entities.UpgradeResult{From: agentVersion, To: req.Version}
	fail := func(format string, args ...any) entities.UpgradeResult {
		res.Message = fmt.Sprintf(format, args...)
		log.Printf("upgrade %s -> %s: %s", agentVersion, req.Version, res.Message)
		return res
	}
	if !upgradeEnabled() {
		return fail("远程升级已在本机关闭（GUEST_UPGRADE=off）")
	}
	if req.Version == agentVersion {
		return fail("已是 %s", agentVersion)
	}
	if req.Artifact.OS != runtime.GOOS || req.Artifact.Arch != runtime.GOARCH {
		return fail("构建平台 %s/%s 与本机 %s/%s 不符", req.Artifact.OS, req.Artifact.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if !upgrading.CompareAndSwap(false, true) {
		return fail("已有升级在进行")
	}
	exe, err := selfExe()
	if err != nil {
		upgrading.Store(false)
		return fail("定位自身路径失败：%v", err)
	}
	staged := exe + ".new"
	setLink(func(s *linkState) { s.Upgrading = req.Version })
	if err := stage(ctx, cfg, req, staged, step); err != nil {
		_ = os.Remove(staged)
		upgrading.Store(false)
		setLink(func(s *linkState) { s.Upgrading = "" })
		return fail("%v", err)
	}
	st := &upgradeState{From: agentVersion, To: req.Version, Mode: req.Mode, Phase: "pending", Started: time.Now(),
		Runner: runnerName(), Exe: exe, Args: os.Args[1:]}
	st.Cwd, _ = os.Getwd()
	go func() {
		time.Sleep(time.Second)
		if err := applyUpgrade(st, staged); err != nil {
			log.Printf("upgrade: apply %s: %v", req.Version, err)
			_ = os.Remove(staged)
			_ = os.Remove(statePath(exe))
			upgrading.Store(false)
			setLink(func(s *linkState) { s.Upgrading = "" })
		}
	}()
	res.OK = true
	log.Printf("upgrade: %s staged and verified, restarting", req.Version)
	return res
}

// progressWriter reports bytes written at most every 300ms.
type progressWriter struct {
	n, total int64
	last     time.Time
	step     func(entities.UpgradeStep)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	if time.Since(w.last) >= 300*time.Millisecond {
		w.last = time.Now()
		w.step(entities.UpgradeStep{Stage: entities.UpgradeDownload, Done: w.n, Total: w.total})
	}
	return len(p), nil
}

func stage(ctx context.Context, cfg config, req entities.UpgradeRequest, dst string, step func(entities.UpgradeStep)) error {
	a := req.Artifact
	if a.Size <= 0 || a.Size > maxArtifact {
		return fmt.Errorf("构建大小 %d 不合理", a.Size)
	}
	keys, err := release.TrustedKeys()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	resp, err := cfg.hubGet(ctx, req.URL, nil)
	if err != nil {
		return fmt.Errorf("下载失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("写入失败：%v", err)
	}
	h := sha256.New()
	pw := &progressWriter{total: a.Size, step: step}
	step(entities.UpgradeStep{Stage: entities.UpgradeDownload, Total: a.Size})
	n, err := io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(resp.Body, maxArtifact+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("下载中断：%v", err)
	}
	step(entities.UpgradeStep{Stage: entities.UpgradeVerify, Done: n, Total: a.Size})
	if err := release.Verify(keys, req.Version, a, n, hex.EncodeToString(h.Sum(nil))); err != nil {
		return fmt.Errorf("校验失败：%v", err)
	}
	step(entities.UpgradeStep{Stage: entities.UpgradeSelfTest, Done: n, Total: a.Size})
	return selfTest(ctx, dst, req.Version)
}

// selfTest runs the staged binary's version command, which proves it executes on
// this OS/arch and is the build it claims to be.
func selfTest(ctx context.Context, bin, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("自检失败：%v %s", err, bytes.TrimSpace(out))
	}
	if !versionBannerOK(out, want) {
		return fmt.Errorf("自检失败：新程序报告 %q", strings.TrimSpace(string(out)))
	}
	return nil
}

// versionBannerOK accepts the name already-deployed agents print (probe-agent)
// and the Vartio name. The version must be the one this upgrade asked for.
func versionBannerOK(out []byte, want string) bool {
	fields := strings.Fields(string(out))
	for i := 0; i+1 < len(fields); i++ {
		if (fields[i] == "probe-agent" || fields[i] == "vartio-agent") && fields[i+1] == want {
			return true
		}
	}
	return false
}

// swapIn moves exe aside to exe.old and staged into place, undoing on failure.
func swapIn(exe, staged string) error {
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		// A previous watchdog may still be running from it on Windows.
		_ = os.Rename(old, fmt.Sprintf("%s.%d", old, time.Now().Unix()))
	}
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}

// swapBack restores exe.old; the rejected build is kept as exe.bad for inspection.
func swapBack(exe string) error {
	bad := exe + ".bad"
	_ = os.Remove(bad)
	if err := os.Rename(exe, bad); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(exe+".old", exe)
}

// autoUpdate polls the hub for a newer build when GUEST_AUTO_UPDATE=on.
func autoUpdate(ctx context.Context, cfg config) {
	if !upgradeEnabled() || !strings.EqualFold(strings.TrimSpace(getenv("VARTIO_AGENT_AUTO_UPDATE", "GUEST_AUTO_UPDATE")), "on") {
		return
	}
	every := 6 * time.Hour
	if d, err := time.ParseDuration(getenv("VARTIO_AGENT_UPDATE_INTERVAL", "GUEST_UPDATE_INTERVAL")); err == nil && d >= time.Minute {
		every = d
	}
	log.Printf("auto update: checking every %s", every)
	first := min(every, 5*time.Minute) / 2
	wait := first + time.Duration(rand.Int64N(int64(first)+1))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = every + time.Duration(rand.Int64N(int64(every/10)+1))
		if pendingReport() != nil || upgrading.Load() {
			continue
		}
		if err := checkLatest(ctx, cfg); err != nil {
			log.Printf("auto update: %v", err)
		}
	}
}

func checkLatest(ctx context.Context, cfg config) error {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := cfg.hubGet(c, entities.PathAgentLatest, url.Values{"os": {runtime.GOOS}, "arch": {runtime.GOARCH}, "current": {agentVersion}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("latest: HTTP %d", resp.StatusCode)
	}
	var l entities.AgentLatest
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		return err
	}
	if release.Compare(l.Version, agentVersion) <= 0 {
		return nil
	}
	log.Printf("auto update: %s available", l.Version)
	if res := upgrade(ctx, cfg, entities.UpgradeRequest{Version: l.Version, Artifact: l.Artifact, URL: l.URL, Mode: "auto"}, nil); !res.OK {
		return errors.New(res.Message)
	}
	return nil
}

// watcher is the platform part of the watchdog: bring the new build up, take it
// down, and bring the previous one back.
type watcher interface {
	start() error
	stop()
	restart() error
}

// runWatch is `probe-agent upgrade-watch <state>`, run from the previous binary.
func runWatch(path string) int {
	st, err := readState(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upgrade-watch:", err)
		return 1
	}
	if f, err := os.OpenFile(st.Exe+".upgrade.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}
	log.Printf("watch: %s -> %s (%s, runner %s)", st.From, st.To, st.Mode, st.Runner)
	w := newWatcher(st)
	reason := ""
	if err := w.start(); err != nil {
		reason = "新版本启动失败：" + err.Error()
	} else {
		deadline := time.Now().Add(probation())
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			if cur, err := readState(path); err != nil || cur.Phase != "pending" {
				log.Printf("watch: %s confirmed", st.To)
				return 0
			}
		}
		reason = fmt.Sprintf("新版本 %s 内未连上 hub", probation())
	}
	log.Printf("watch: rolling back: %s", reason)
	w.stop()
	if err := swapBack(st.Exe); err != nil {
		log.Printf("watch: restore previous binary: %v", err)
		return 1
	}
	st.Phase, st.Error = "rolled_back", reason
	if err := writeState(path, st); err != nil {
		log.Printf("watch: write state: %v", err)
	}
	if err := w.restart(); err != nil {
		log.Printf("watch: restart %s: %v", st.From, err)
		return 1
	}
	log.Printf("watch: %s restored", st.From)
	return 0
}
