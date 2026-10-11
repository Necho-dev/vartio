package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	msg, _ := json.Marshal(map[string]any{
		"listen": ln.Addr().String(),
		"ui":     true,
		"manifest": map[string]any{
			"name": "hello", "title": "Hello", "version": "0.0.1",
			"primitives": []string{"tunnel.tcp"},
		},
	})
	fmt.Printf("%s\n", msg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/probe/plugins/hello/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})
	mux.HandleFunc("GET /plugins/hello/index.js", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("export function mount() {}"))
	})
	_ = http.Serve(ln, mux)
}
