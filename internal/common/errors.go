// Package common holds the API error type shared by the hub's HTTP surface.
package common

import (
	"fmt"
	"net/http"
)

// Error is an API-facing fault with an HTTP status and a stable code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func errf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func ErrBad(format string, args ...any) *Error {
	return errf(http.StatusBadRequest, "bad_request", format, args...)
}

func ErrOffline() *Error {
	return errf(http.StatusConflict, "guest_offline", "guest agent not connected")
}

func ErrUnbound(format string, args ...any) *Error {
	return errf(http.StatusConflict, "guest_unbound", format, args...)
}

func ErrTooLarge(format string, args ...any) *Error {
	return errf(http.StatusRequestEntityTooLarge, "too_large", format, args...)
}

func RequireConfirm(confirm bool) error {
	if confirm {
		return nil
	}
	return errf(http.StatusForbidden, "confirm_required", "destructive action requires confirm=true or header X-Confirm: true")
}
