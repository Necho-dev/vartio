package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemoveOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "probe-agent")
	for _, p := range []string{exe, exe + ".old", exe + ".old.1700000000", exe + ".bad"} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	removeOld(exe)
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 2 || left[0] != exe || left[1] != exe+".bad" {
		t.Fatalf("left %v", left)
	}
}

func TestSwapInAndBack(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "probe-agent")
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	write(exe, "v1")
	write(exe+".old", "stale")
	write(exe+".new", "v2")
	if err := swapIn(exe, exe+".new"); err != nil {
		t.Fatal(err)
	}
	if read(exe) != "v2" || read(exe+".old") != "v1" {
		t.Fatalf("after swapIn: exe=%q old=%q", read(exe), read(exe+".old"))
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal("staged file left behind")
	}
	if err := swapBack(exe); err != nil {
		t.Fatal(err)
	}
	if read(exe) != "v1" || read(exe+".bad") != "v2" {
		t.Fatalf("after swapBack: exe=%q bad=%q", read(exe), read(exe+".bad"))
	}
}

func TestSwapInMissingStagedKeepsExe(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "probe-agent")
	_ = os.WriteFile(exe, []byte("v1"), 0o755)
	if err := swapIn(exe, exe+".new"); err == nil {
		t.Fatal("swapIn without a staged build succeeded")
	}
	if b, _ := os.ReadFile(exe); string(b) != "v1" {
		t.Fatalf("exe not restored: %q", b)
	}
}

func TestHubBase(t *testing.T) {
	for in, want := range map[string]string{
		"ws://10.0.0.1:8766/v1/probe/connect":        "http://10.0.0.1:8766",
		"wss://hub.example.com/v1/probe/connect?x=1": "https://hub.example.com",
	} {
		u, err := hubBase(in)
		if err != nil || u.String() != want {
			t.Errorf("hubBase(%s) = %v, %v; want %s", in, u, err, want)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	in := &upgradeState{From: "1.9.0", To: "1.9.1", Phase: "pending", PID: 42, Args: []string{"-x"}}
	if err := writeState(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := readState(p)
	if err != nil || out.To != "1.9.1" || out.PID != 42 || len(out.Args) != 1 {
		t.Fatalf("readState = %+v, %v", out, err)
	}
}

// selfTest must accept this very binary's version line and reject a mismatch.
func TestSelfTestRejectsWrongVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	bin := filepath.Join(t.TempDir(), "fake")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\necho probe-agent 1.9.1 test/test\n"), 0o755)
	if err := selfTest(context.Background(), bin, "1.9.1"); err != nil {
		t.Fatalf("matching version rejected: %v", err)
	}
	if err := selfTest(context.Background(), bin, "2.0.0"); err == nil || !strings.Contains(err.Error(), "自检失败") {
		t.Fatalf("mismatch accepted: %v", err)
	}
}
