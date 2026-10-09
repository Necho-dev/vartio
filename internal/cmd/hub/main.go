// probe-hub: standalone probe control plane with a fleet monitoring UI.
// No ESXi and no .env: config comes from the environment or probe-hub.env.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"govmomi-wmks/internal/guest"
	"govmomi-wmks/internal/monitor"
)

//go:embed web
var webFS embed.FS

func main() {
	loadEnv()
	listen := firstEnv("PROBE_HUB_LISTEN", "GUEST_DEV_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8766"
	}
	token := os.Getenv("GUEST_TOKEN")
	if token == "" {
		token = "dev"
	}
	admin := strings.TrimSpace(os.Getenv("PROBE_HUB_ADMIN_TOKEN"))
	interval := envDuration("PROBE_HUB_INTERVAL", 10*time.Second)
	retention := envDuration("PROBE_HUB_RETENTION", 7*24*time.Hour)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var store *monitor.Store
	if dsn := strings.TrimSpace(os.Getenv("PROBE_HUB_DSN")); dsn != "" {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		s, err := monitor.OpenStore(c, dsn)
		cancel()
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		defer s.Close()
		store = s
	} else {
		log.Printf("PROBE_HUB_DSN not set: live view only, no history")
	}

	hub := guest.NewHub(token)
	mon := monitor.New(hub, store, interval, retention)
	if err := mon.Load(ctx); err != nil {
		log.Fatalf("load systems: %v", err)
	}
	go mon.Run(ctx)

	mux := http.NewServeMux()
	hub.RegisterDev(mux)
	mon.Register(mux, admin != "")
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServerFS(sub)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))

	srv := &http.Server{Addr: listen, Handler: monitor.RequireToken(admin, mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	log.Printf("probe-hub http://%s  ui=/  agents=%s  interval=%s retention=%s history=%v auth=%v",
		listen, guest.PathConnect, interval, retention, store != nil, admin != "")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func loadEnv() {
	_ = godotenv.Load("probe-hub.env")
	if exe, err := os.Executable(); err == nil {
		_ = godotenv.Load(filepath.Join(filepath.Dir(exe), "probe-hub.env"))
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("%s=%q invalid, using %s", key, v, def)
		return def
	}
	return d
}
