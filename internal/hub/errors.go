package guest

import (
	"context"
	"errors"
	"net/http"

	"govmomi-wmks/internal/esxi"
)

var errOffline = errors.New("guest agent not connected")

type RPCError struct {
	Code    string
	Message string
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

func APIError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errOffline) {
		return esxi.ErrGuestOffline()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &esxi.Error{Status: http.StatusGatewayTimeout, Code: "timeout", Message: "guest rpc timeout"}
	}
	var rpc *RPCError
	if errors.As(err, &rpc) {
		status := http.StatusBadGateway
		switch rpc.Code {
		case "not_implemented":
			status = http.StatusNotImplemented
		case "timeout":
			status = http.StatusGatewayTimeout
		case "bad_request":
			status = http.StatusBadRequest
		case "not_supported", "no_user_session", "conflict", "too_many_terms", "too_many_tunnels":
			status = http.StatusConflict
		case "agent_policy":
			status = http.StatusForbidden
		case "not_found":
			status = http.StatusNotFound
		case "too_large":
			status = http.StatusRequestEntityTooLarge
		case "confirm_required":
			status = http.StatusForbidden
		}
		return &esxi.Error{Status: status, Code: rpc.Code, Message: rpc.Message}
	}
	return &esxi.Error{Status: http.StatusBadGateway, Code: "guest_rpc", Message: err.Error()}
}
