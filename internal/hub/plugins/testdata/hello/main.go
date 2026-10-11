package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
)

func main() {
	raw, err := os.ReadFile(os.Getenv("VARTIO_PLUGIN_MANIFEST"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ui, _ := manifest["ui"].(bool)
	name, _ := manifest["name"].(string)
	msg, _ := json.Marshal(map[string]any{
		"listen":   ln.Addr().String(),
		"ui":       ui,
		"manifest": json.RawMessage(raw),
	})
	fmt.Printf("%s\n", msg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/probe/plugins/"+name+"/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})
	mux.HandleFunc("GET /plugins/"+name+"/index.js", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("export function mount() {}"))
	})
	_ = http.Serve(ln, mux)
}
