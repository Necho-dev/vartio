package main

import (
	"encoding/json"
	"errors"

	"govmomi-wmks/internal/guest"
)

func (a *agent) dispatch(fr guest.Frame) (any, *guest.FrameError) {
	switch fr.Method {
	case guest.MethodHealth:
		h, err := collectHealth(a.cfg.Watches)
		return h, asRPC(err)
	case guest.MethodMetrics:
		return collectMetrics(), nil
	case guest.MethodProcsList:
		list, err := listProcs()
		return list, asRPC(err)
	case guest.MethodProcsAction:
		var req guest.ActionRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return procAction(req)
	case guest.MethodServicesList:
		list, err := listServices()
		return list, asRPC(err)
	case guest.MethodServicesAction:
		var req guest.ActionRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return serviceAction(req)
	case guest.MethodExec:
		var req guest.ExecRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return runExec(req), nil
	case guest.MethodTermOpen:
		var req guest.TermOpenRequest
		_ = json.Unmarshal(fr.Params, &req)
		id := fr.ID
		if err := a.term.open(id, req, func(p []byte) { a.streamOut(id, p) }, func() { a.streamExit(id) }); err != nil {
			return nil, asRPC(err)
		}
		return guest.TermOpenResult{OK: true}, nil
	case guest.MethodTermResize:
		var req guest.TermOpenRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		a.term.setSize(req.ID, req.Cols, req.Rows)
		return map[string]bool{"ok": true}, nil
	case guest.MethodTermClose:
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(fr.Params, &req)
		a.term.close(req.ID)
		return map[string]bool{"ok": true}, nil
	case guest.MethodPower:
		var req guest.PowerRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return doPower(req.Action)
	case guest.MethodDisplayOpen:
		if err := a.display.open(func(p []byte) { a.streamDisplay(fr.ID, p) }); err != nil {
			return nil, err
		}
		return guest.DisplayOpenResult{OK: true, FPS: guest.DisplayFPS, MaxSide: guest.DisplayMaxSide}, nil
	case guest.MethodDisplayClose:
		a.display.close()
		return map[string]bool{"ok": true}, nil
	case guest.MethodFilesList:
		return filesList(fr.Params)
	case guest.MethodFilesGet:
		return filesGet(fr.Params)
	case guest.MethodFilesPut:
		return filesPut(fr.Params)
	case guest.MethodFilesOp:
		return filesOp(fr.Params)
	case guest.MethodTrash:
		return trashOp(fr.Params)
	default:
		return nil, &guest.FrameError{Code: "not_implemented", Message: fr.Method}
	}
}

func asRPC(err error) *guest.FrameError {
	if err == nil {
		return nil
	}
	var fe *guest.FrameError
	if errors.As(err, &fe) {
		return fe
	}
	return &guest.FrameError{Code: "error", Message: err.Error()}
}

func bad(msg string) *guest.FrameError {
	return &guest.FrameError{Code: "bad_request", Message: msg}
}
