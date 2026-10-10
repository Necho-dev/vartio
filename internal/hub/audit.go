package guest

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"govmomi-wmks/internal/esxi"
)

// record audits an operator action from the probe API. Requests turned away before
// they reach the agent (validation, missing confirm, offline) are not actions.
func (h *Hub) record(r *http.Request, kind, detail string, err error) {
	if h.OnAudit == nil {
		return
	}
	var api *esxi.Error
	if errors.As(err, &api) {
		switch api.Code {
		case "bad_request", "confirm_required", "guest_offline", "guest_unbound":
			return
		}
	}
	h.audit(r.PathValue("id"), kind, detail, clientIP(r), err)
}

func execLine(req ExecRequest) string {
	line := strings.TrimSpace(req.Command + " " + strings.Join(req.Args, " "))
	if len(line) > 300 {
		line = line[:300] + "…"
	}
	return line
}

func sizeNote(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KB"
	}
	return strconv.FormatInt(n, 10) + " B"
}
