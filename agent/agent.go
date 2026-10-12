package agent

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

	"vartio"
	"vartio/internal/entities"
)

// agentVersion follows vartio.Version. Override the stamp with -ldflags -X vartio.Version.
// init reads this after the linker stamp is applied, so it must not be a const.
var agentVersion = vartio.Version

func Run() {
	if len(os.Args) > 1 && os.Args[1] != "upgrade-watch" && !(len(os.Args) > 2 && os.Args[1] == "service" && os.Args[2] == "run") {
		attachConsole()
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			// The first line stays probe-agent so agents already in the field
			// accept this build: their self-test requires that exact name.
			fmt.Printf("probe-agent %s %s/%s\n", agentVersion, runtime.GOOS, runtime.GOARCH)
			fmt.Printf("vartio-agent %s %s/%s\n", agentVersion, runtime.GOOS, runtime.GOARCH)
			return
		case "upgrade-watch":
			if len(os.Args) != 3 {
				fmt.Fprintln(os.Stderr, "usage: vartio-agent upgrade-watch <state file>")
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
	log.Printf("vartio-agent %s control=%s device=%s bios=%s runner=%s tray=%t", agentVersion, cfg.URL, cfg.DeviceID, cfg.BiosUUID, runnerName(), trayMode)
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
	names := []string{"vartio-agent.env", "probe-agent.env", "agent.env"}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, name := range names {
			_ = godotenv.Load(filepath.Join(dir, name))
		}
	}
	for _, name := range names {
		_ = godotenv.Load(name)
	}
}

type config struct {
	URL      string
	Token    string
	BiosUUID string
	DeviceID string
	Device   *entities.DeviceFacts
	Watches  []watchSpec
}

type watchSpec struct {
	Kind string
	Name string
}

func loadConfig() config {
	url := strings.TrimSpace(getenv("VARTIO_AGENT_CONTROL_URL", "GUEST_CONTROL_URL"))
	if url == "" {
		url = "ws://127.0.0.1:8766/v1/probe/connect"
	}
	token := getenv("VARTIO_AGENT_TOKEN", "GUEST_TOKEN")
	if token == "" {
		token = "dev"
	}
	bios := strings.TrimSpace(getenv("VARTIO_AGENT_BIOS_UUID", "GUEST_BIOS_UUID"))
	if bios == "" {
		if id, err := readBIOSUUID(); err == nil {
			bios = id
		} else {
			log.Printf("bios uuid: %v (set GUEST_BIOS_UUID)", err)
			bios = "unknown"
		}
	}
	var watches []watchSpec
	for _, n := range splitCSV(getenv("VARTIO_AGENT_WATCH_PROCS", "GUEST_WATCH_PROCS")) {
		watches = append(watches, watchSpec{Kind: "process", Name: n})
	}
	for _, n := range splitCSV(getenv("VARTIO_AGENT_WATCH_SERVICES", "GUEST_WATCH_SERVICES")) {
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
