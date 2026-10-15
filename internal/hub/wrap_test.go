package hub

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vartio/internal/common"
)

func TestWrapLeavesAStartedResponseAlone(t *testing.T) {
	h := &Hub{}
	started := h.wrap(func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		return errors.New("agent lost the file")
	})
	rec := httptest.NewRecorder()
	started(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Fatalf("started response was rewritten: %d %q", rec.Code, rec.Body.String())
	}

	failed := h.wrap(func(http.ResponseWriter, *http.Request) error {
		return common.ErrBad("path required")
	})
	rec = httptest.NewRecorder()
	failed(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "path required") {
		t.Fatalf("error before any output: %d %q", rec.Code, rec.Body.String())
	}
}
