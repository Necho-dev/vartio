// vartio: standalone probe control plane with a fleet monitoring UI.
// No ESXi. Config comes from the environment, vartio.env, or probe-hub.env.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"vartio/internal/entities"
	"vartio/internal/hub"
	"vartio/internal/hub/plugins"
	"vartio/internal/hub/systems"
	"vartio/internal/site"
)

func main() {
	loadEnv()
	listen := firstEnv("VARTIO_LISTEN", "PROBE_HUB_LISTEN", "GUEST_DEV_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8766"
	}
	token := firstEnv("VARTIO_TOKEN", "GUEST_TOKEN")
	if token == "" {
		token = "dev"
	}
	admin := firstEnv("VARTIO_ADMIN_TOKEN", "PROBE_HUB_ADMIN_TOKEN")
	interval := envDuration(10*time.Second, "VARTIO_INTERVAL", "PROBE_HUB_INTERVAL")
	retention := envDuration(7*24*time.Hour, "VARTIO_RETENTION", "PROBE_HUB_RETENTION")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var store *systems.Store
	if dsn := firstEnv("VARTIO_DSN", "PROBE_HUB_DSN"); dsn != "" {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		s, err := systems.OpenStore(c, dsn)
		cancel()
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		defer s.Close()
		store = s
	} else {
		log.Printf("VARTIO_DSN not set: live view only, no history")
	}

	h := hub.NewHub(token)
	h.Releases = &hub.Releases{Dir: agentDir()}
	if !strings.EqualFold(firstEnv("VARTIO_AUTH", "PROBE_HUB_AUTH"), "off") {
		auth, err := hub.LoadAuth(
			stateFile([]string{"VARTIO_KEY_FILE", "PROBE_HUB_KEY_FILE"}, "vartio.key", "probe-hub.key"),
			stateFile([]string{"VARTIO_CREDENTIALS_FILE", "PROBE_HUB_CREDENTIALS_FILE"}, "vartio.agents.json", "probe-hub.agents.json"),
		)
		if err != nil {
			log.Fatalf("hub key: %v", err)
		}
		auth.Enroll = !strings.EqualFold(firstEnv("VARTIO_ENROLL", "PROBE_HUB_ENROLL"), "off")
		h.Auth = auth
		log.Printf("hub public key %s (VARTIO_AGENT_HUB_KEY)  enroll=%v", auth.PublicKey(), auth.Enroll)
	}
	if err := h.LoadForwards(stateFile([]string{"VARTIO_FORWARDS_FILE", "PROBE_HUB_FORWARDS_FILE"}, "vartio.forwards.json", "probe-hub.forwards.json")); err != nil {
		log.Fatalf("forwards: %v", err)
	}
	mon := systems.New(h, store, interval, retention)
	mon.Retention5m = envDuration(mon.Retention5m, "VARTIO_RETENTION_5M", "PROBE_HUB_RETENTION_5M")
	mon.Retention1h = envDuration(mon.Retention1h, "VARTIO_RETENTION_1H", "PROBE_HUB_RETENTION_1H")
	if err := mon.Load(ctx); err != nil {
		log.Fatalf("load systems: %v", err)
	}
	go mon.Run(ctx)

	reg := plugins.NewRegistry(h)
	reg.Dir = pluginDir()
	if err := reg.Load(stateFile([]string{"VARTIO_PLUGINS_FILE", "PROBE_HUB_PLUGINS_FILE"}, "vartio.plugins.json", "probe-hub.plugins.json"), splitList(firstEnv("VARTIO_PLUGINS", "PROBE_HUB_PLUGINS"))); err != nil {
		log.Fatalf("plugins: %v", err)
	}
	go reg.Restore(ctx)

	mux := http.NewServeMux()
	h.RegisterDev(mux)
	mon.Register(mux, admin != "")
	reg.Mount(mux)
	static := http.FileServerFS(site.FS)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))

	srv := &http.Server{Addr: listen, Handler: systems.RequireToken(admin, mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	log.Printf("vartio http://%s  ui=/  agents=%s  interval=%s retention=%s history=%v auth=%v releases=%s",
		listen, entities.PathConnect, interval, retention, store != nil, admin != "", h.Releases.Dir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func loadEnv() {
	for _, name := range []string{"vartio.env", "probe-hub.env"} {
		_ = godotenv.Load(name)
		if exe, err := os.Executable(); err == nil {
			_ = godotenv.Load(filepath.Join(filepath.Dir(exe), name))
		}
	}
}

// pluginDir is where downloaded plugin repositories are built and kept.
func pluginDir() string {
	if d := firstEnv("VARTIO_PLUGIN_DIR"); d != "" {
		return d
	}
	return "vartio-plugins"
}

// agentDir holds the signed agent builds the hub offers.
func agentDir() string {
	if d := firstEnv("VARTIO_AGENT_DIR", "PROBE_HUB_AGENT_DIR"); d != "" {
		return d
	}
	for _, d := range []string{
		filepath.Join("dist", "vartio-agent"),
		filepath.Join("dist", "probe-agent"),
		filepath.Join(exeDir(), "vartio-agent"),
		filepath.Join(exeDir(), "probe-agent"),
	} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return ""
}

// stateFile is where the hub remembers keys, plugins and port forwards.
// The first existing name wins, so a copied probe-hub.key keeps the same identity.
// VARTIO_PLUGINS (comma-separated names) only seeds plugins the first time.
func stateFile(keys []string, names ...string) string {
	if p := firstEnv(keys...); p != "" {
		return p
	}
	for _, name := range names {
		if st, err := os.Stat(name); err == nil && !st.IsDir() {
			return name
		}
	}
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func envDuration(def time.Duration, keys ...string) time.Duration {
	v := firstEnv(keys...)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("%s=%q invalid, using %s", strings.Join(keys, "/"), v, def)
		return def
	}
	return d
}
