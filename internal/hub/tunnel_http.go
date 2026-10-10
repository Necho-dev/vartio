package guest

import "net/http"

// RegisterTunnels mounts the tunnel listing under prefix + "/{id}", where id is
// a device id or a BIOS UUID, and the port forwards at PathForwards.
func (h *Hub) RegisterTunnels(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"/{id}/tunnels", h.wrap(h.handleTunnelsList))
	h.registerForwards(mux)
}

func (h *Hub) handleTunnelsList(w http.ResponseWriter, r *http.Request) error {
	return writeJSON(w, map[string]any{"tunnels": h.Tunnels(r.PathValue("id"))})
}
