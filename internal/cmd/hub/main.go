// probe-hub: standalone probe control plane. No ESXi, no .env, not :8765.
package main

import (
	"log"
	"net/http"
	"os"

	"govmomi-wmks/internal/guest"
)

func main() {
	listen := os.Getenv("PROBE_HUB_LISTEN")
	if listen == "" {
		listen = os.Getenv("GUEST_DEV_LISTEN")
	}
	if listen == "" {
		listen = "127.0.0.1:8766"
	}
	token := os.Getenv("GUEST_TOKEN")
	if token == "" {
		token = "dev"
	}
	h := guest.NewHub(token)
	log.Printf("probe-hub http://%s  /v1/probe/connect  (not vm-server :8765)", listen)
	log.Fatal(http.ListenAndServe(listen, h.DevHandler()))
}
