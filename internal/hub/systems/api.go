package monitor

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"govmomi-wmks/internal/esxi"
	"govmomi-wmks/internal/guest"
)

const (
	PathMonitor = guest.PathPrefix + "/monitor"
	PathConfig  = PathMonitor + "/config"
	PathSystems = PathMonitor + "/systems"
)

type Config struct {
	IntervalSec  int  `json:"intervalSec"`
	RetentionSec int  `json:"retentionSec"`
	History      bool `json:"history"`
	AuthRequired bool `json:"authRequired"`
}

type detail struct {
	System
	Events []Event `json:"events"`
}

// Register mounts the monitor API on mux. Auth is applied by the caller's middleware.
func (m *Monitor) Register(mux *http.ServeMux, authRequired bool) {
	mux.HandleFunc("GET "+PathConfig, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, Config{
			IntervalSec:  int(m.Interval / time.Second),
			RetentionSec: int(m.Retention / time.Second),
			History:      m.Store != nil,
			AuthRequired: authRequired,
		})
	})
	mux.HandleFunc("GET "+PathSystems, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.List())
	})
	mux.HandleFunc("GET "+PathSystems+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		sys, ok := m.Get(r.PathValue("id"))
		if !ok {
			writeErr(w, errNotFound)
			return
		}
		out := detail{System: sys, Events: []Event{}}
		if m.Store != nil {
			if ev, err := m.Store.Events(r.Context(), sys.ID, "", 50); err == nil {
				out.Events = ev
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET "+PathSystems+"/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		sys, ok := m.Get(r.PathValue("id"))
		if !ok {
			writeErr(w, errNotFound)
			return
		}
		if m.Store == nil {
			writeJSON(w, http.StatusOK, []Event{})
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		ev, err := m.Store.Events(r.Context(), sys.ID, r.URL.Query().Get("category"), limit)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ev)
	})
	mux.HandleFunc("PATCH "+PathSystems+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name *string  `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, esxi.ErrBad("invalid json"))
			return
		}
		sys, err := m.Update(r.Context(), r.PathValue("id"), clientIP(r), body.Name, body.Tags)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sys)
	})
	mux.HandleFunc("DELETE "+PathSystems+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.Delete(r.Context(), r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET "+PathSystems+"/{id}/series", func(w http.ResponseWriter, r *http.Request) {
		span, ok := ranges[r.URL.Query().Get("range")]
		if !ok {
			span = time.Hour
		}
		step := stepFor(span, m.Interval)
		if m.Store == nil {
			writeJSON(w, http.StatusOK, newSeries(step))
			return
		}
		sys, ok := m.Get(r.PathValue("id"))
		if !ok {
			writeErr(w, errNotFound)
			return
		}
		out, err := m.Store.Series(r.Context(), sys.ID, time.Now().Add(-span), step)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// RequireToken guards operator paths. Agent connect (guest token) and one-shot stream
// tickets stay reachable; static UI assets are public and log in from the browser.
func RequireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		protected := strings.HasPrefix(p, guest.PathPrefix+"/") && !guest.IsPublicProbePath(p) && p != PathConfig
		if protected {
			got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if got == "" {
				got = r.Header.Get("X-Admin-Token")
			}
			if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				writeErr(w, &esxi.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "admin token required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var api *esxi.Error
	switch {
	case errors.As(err, &api):
	case errors.Is(err, errNotFound):
		api = &esxi.Error{Status: http.StatusNotFound, Code: "not_found", Message: err.Error()}
	default:
		api = &esxi.Error{Status: http.StatusInternalServerError, Code: "error", Message: err.Error()}
	}
	writeJSON(w, api.Status, map[string]string{"code": api.Code, "message": api.Message})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
