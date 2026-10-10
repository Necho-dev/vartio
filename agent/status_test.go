package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

func TestRotLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.log")
	l, err := openLog(p)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 1<<20) + "\n")
	for i := 0; i < 12; i++ {
		if _, err := l.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	cur, _ := os.Stat(p)
	old, err := os.Stat(p + ".1")
	if err != nil {
		t.Fatal("no rotated file:", err)
	}
	if cur.Size() > logMax || old.Size() > logMax {
		t.Fatalf("sizes %d / %d exceed %d", cur.Size(), old.Size(), logMax)
	}
}

func TestSaveEnvFile(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "probe-agent")
	t.Setenv("GUEST_CONTROL_URL", "ws://hub:8766/v1/probe/connect")
	t.Setenv("GUEST_TOKEN", `a"b\c`)
	p, err := saveEnvFile(exe)
	if err != nil || p == "" {
		t.Fatalf("saveEnvFile = %q, %v", p, err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	m, err := godotenv.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["GUEST_TOKEN"] != `a"b\c` || m["GUEST_CONTROL_URL"] != "ws://hub:8766/v1/probe/connect" {
		t.Fatalf("round trip: %v", m)
	}
	if p2, _ := saveEnvFile(exe); p2 != "" {
		t.Fatal("overwrote an existing env file")
	}
}

func TestLinkStateText(t *testing.T) {
	cases := []struct {
		s     linkState
		state string
		text  string
	}{
		{linkState{Dialing: true}, "off", "连接中"},
		{linkState{Err: "dial tcp: connection refused"}, "off", "未连接：dial tcp"},
		{linkState{Connected: true}, "ok", "已连接"},
		{linkState{Probation: "1.10.0"}, "up", "等待连上 hub"},
		{linkState{Connected: true, Upgrading: "1.10.1"}, "up", "正在升级到 1.10.1"},
	}
	for _, c := range cases {
		text, state := trayText(c.s)
		if state != c.state || !strings.Contains(text, c.text) {
			t.Errorf("%+v: got %q/%s, want %q/%s", c.s, text, state, c.text, c.state)
		}
	}
}
