package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"govmomi-wmks/internal/guest"
)

// GUEST_SPAWN=off withdraws proc.spawn; exec stays governed by its own rules.
func spawnEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("GUEST_SPAWN")), "off")
}

var winVar = regexp.MustCompile(`%([A-Za-z0-9_()]+)%`)

// expandPath expands a leading ~, %VAR% (any OS) and $VAR / ${VAR}. Unknown
// %VAR% stay as written so a missing variable fails the lookup visibly.
func expandPath(s string) string {
	if homePath(s) {
		if home, err := os.UserHomeDir(); err == nil {
			s = home + s[1:]
		}
	}
	s = winVar.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
	if strings.Contains(s, "$") {
		s = os.ExpandEnv(s)
	}
	return filepath.FromSlash(s)
}

func homePath(s string) bool {
	return s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`)
}

// expandArg expands a leading ~ (also in --flag=~/...) and %VAR%, but not
// $VAR or slashes: arguments may be scripts or URLs.
func expandArg(s string) string {
	if homePath(s) {
		return expandPath(s)
	}
	if i := strings.Index(s, "=~"); i > 0 && strings.HasPrefix(s, "-") && homePath(s[i+1:]) {
		return s[:i+1] + expandPath(s[i+1:])
	}
	return winVar.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
}

func which(req guest.WhichRequest) guest.WhichResult {
	out := guest.WhichResult{Hits: []guest.WhichHit{}}
	seen := map[string]bool{}
	add := func(q, p string) {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || seen[p] {
			return
		}
		seen[p] = true
		out.Hits = append(out.Hits, guest.WhichHit{Query: q, Path: p, Size: st.Size(), MtimeUTC: st.ModTime().UTC().Format(time.RFC3339)})
	}
	for _, q := range req.Paths {
		add(q, expandPath(q))
	}
	for _, n := range req.Names {
		if p, err := exec.LookPath(n); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			add(n, p)
		}
	}
	return out
}

func readFiles(req guest.ReadRequest) guest.ReadResult {
	limit := req.Limit
	if limit <= 0 {
		limit = 64 << 10
	}
	limit = min(limit, guest.ReadMax)
	out := guest.ReadResult{Files: []guest.ReadFile{}}
	for _, q := range req.Paths {
		p := expandPath(q)
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			f.Close()
			continue
		}
		buf := make([]byte, limit+1)
		n, _ := io.ReadFull(f, buf)
		f.Close()
		rf := guest.ReadFile{Query: q, Path: p, Size: st.Size(), MtimeUTC: st.ModTime().UTC().Format(time.RFC3339), Data: buf[:min(n, limit)]}
		rf.Truncated = n > limit
		out.Files = append(out.Files, rf)
	}
	return out
}

func spawnProc(req guest.SpawnRequest) (guest.SpawnResult, *guest.FrameError) {
	if !spawnEnabled() {
		return guest.SpawnResult{}, &guest.FrameError{Code: "agent_policy", Message: "proc.spawn is disabled by GUEST_SPAWN=off"}
	}
	path := expandPath(strings.TrimSpace(req.Path))
	if path == "" {
		return guest.SpawnResult{}, bad("path is required")
	}
	if !filepath.IsAbs(path) {
		p, err := exec.LookPath(path)
		if err != nil {
			return guest.SpawnResult{}, &guest.FrameError{Code: "not_found", Message: err.Error()}
		}
		path = p
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return guest.SpawnResult{}, &guest.FrameError{Code: "not_found", Message: path + " is not an executable file"}
	}
	args := make([]string, len(req.Args))
	for i, a := range req.Args {
		args[i] = expandArg(a)
	}
	cmd := exec.Command(path, args...)
	if req.Cwd != "" {
		cmd.Dir = expandPath(req.Cwd)
	}
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		keys := make([]string, 0, len(req.Env))
		for k := range req.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			cmd.Env = append(cmd.Env, k+"="+req.Env[k])
		}
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return guest.SpawnResult{}, &guest.FrameError{Code: "spawn_failed", Message: err.Error()}
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return guest.SpawnResult{PID: pid, Path: path, Args: args}, nil
}

// listeners keeps only processes whose name contains one of req.Names.
func listeners(ctx context.Context, req guest.ListenersRequest) ([]guest.Listener, error) {
	all, err := listenSockets(ctx)
	if err != nil {
		return nil, err
	}
	out := []guest.Listener{}
	seen := map[guest.Listener]bool{}
	for _, l := range all {
		if seen[l] || !nameMatches(l.Name, req.Names) {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].IP < out[j].IP
	})
	return out, nil
}

func nameMatches(name string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	n := strings.ToLower(name)
	for _, w := range want {
		if w != "" && strings.Contains(n, strings.ToLower(w)) {
			return true
		}
	}
	return false
}
