package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"govmomi-wmks/internal/guest"
)

// agentVersion is a var so test builds can override it with -ldflags -X.
var agentVersion = "1.18.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] != "upgrade-watch" && !(len(os.Args) > 2 && os.Args[1] == "service" && os.Args[2] == "run") {
		attachConsole()
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("probe-agent %s %s/%s\n", agentVersion, runtime.GOOS, runtime.GOARCH)
			return
		case "upgrade-watch":
			if len(os.Args) != 3 {
				fmt.Fprintln(os.Stderr, "usage: probe-agent upgrade-watch <state file>")
				os.Exit(2)
			}
			os.Exit(runWatch(os.Args[2]))
		}
	}
	loadEnv()
	applyHostRoot()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "service":
			os.Exit(runServiceCommand(os.Args[2:]))
		case "install":
			os.Exit(runInstall())
		case "uninstall":
			os.Exit(runUninstall())
		}
	}
	trayMode = trayWanted()
	if trayMode {
		if err := singleInstance(); err != nil {
			notifyUser(err.Error())
			return
		}
	}
	logFile := setupLog()
	cfg := loadConfig()
	log.Printf("probe-agent %s control=%s device=%s bios=%s runner=%s", agentVersion, cfg.URL, cfg.DeviceID, cfg.BiosUUID, runnerName())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if trayMode {
		runTray(ctx, cfg, logFile)
		return
	}
	if err := serve(ctx, cfg); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func loadEnv() {
	_ = godotenv.Load()
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		_ = godotenv.Load(filepath.Join(dir, "probe-agent.env"))
		_ = godotenv.Load(filepath.Join(dir, "agent.env"))
	}
	_ = godotenv.Load("probe-agent.env")
	_ = godotenv.Load("agent.env")
}

type config struct {
	URL      string
	Token    string
	BiosUUID string
	DeviceID string
	Device   *guest.DeviceFacts
	Watches  []watchSpec
}

type watchSpec struct {
	Kind string
	Name string
}

func loadConfig() config {
	url := strings.TrimSpace(os.Getenv("GUEST_CONTROL_URL"))
	if url == "" {
		url = "ws://127.0.0.1:8766/v1/probe/connect"
	}
	token := os.Getenv("GUEST_TOKEN")
	if token == "" {
		token = "dev"
	}
	bios := strings.TrimSpace(os.Getenv("GUEST_BIOS_UUID"))
	if bios == "" {
		if id, err := readBIOSUUID(); err == nil {
			bios = id
		} else {
			log.Printf("bios uuid: %v (set GUEST_BIOS_UUID)", err)
			bios = "unknown"
		}
	}
	var watches []watchSpec
	for _, n := range splitCSV(os.Getenv("GUEST_WATCH_PROCS")) {
		watches = append(watches, watchSpec{Kind: "process", Name: n})
	}
	for _, n := range splitCSV(os.Getenv("GUEST_WATCH_SERVICES")) {
		watches = append(watches, watchSpec{Kind: "service", Name: n})
	}
	id, facts := deviceID()
	return config{URL: url, Token: token, BiosUUID: bios, DeviceID: id, Device: facts, Watches: watches}
}

func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
