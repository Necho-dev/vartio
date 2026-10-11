package main

import (
	"context"
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
	case guest.MethodUpgrade:
		var req guest.UpgradeRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return upgrade(context.Background(), a.cfg, req, func(st guest.UpgradeStep) { a.streamUpgrade(fr.ID, st) }), nil
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
		var req guest.DisplayOpenRequest
		_ = json.Unmarshal(fr.Params, &req)
		res, err := a.display.open(fr.ID, req, func(p []byte) { a.streamDisplay(fr.ID, p) })
		if err != nil {
			return nil, err
		}
		return res, nil
	case guest.MethodDisplaySelect:
		var req guest.DisplayOpenRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		if err := a.display.selectScreen(req.ID, req.Screen); err != nil {
			return nil, err
		}
		return map[string]int{"screen": req.Screen}, nil
	case guest.MethodDisplayQuality:
		var req guest.DisplayOpenRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return map[string]string{"quality": a.display.setQuality(req.ID, req.Quality)}, nil
	case guest.MethodDisplayClose:
		var req guest.DisplayOpenRequest
		_ = json.Unmarshal(fr.Params, &req)
		a.display.close(req.ID)
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
	case guest.MethodTunnelOpen:
		return a.tunnelOpen(fr.Params)
	case guest.MethodFsWhich:
		var req guest.WhichRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return which(req), nil
	case guest.MethodFsRead:
		var req guest.ReadRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return readFiles(req), nil
	case guest.MethodProcSpawn:
		var req guest.SpawnRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return spawnProc(req)
	case guest.MethodNetListeners:
		var req guest.ListenersRequest
		_ = json.Unmarshal(fr.Params, &req)
		ctx, cancel := context.WithTimeout(context.Background(), guest.TimeoutFor(guest.MethodNetListeners, 0))
		defer cancel()
		list, err := listeners(ctx, req)
		return list, asRPC(err)
	case guest.MethodDockerInspect:
		var req guest.DockerInspectRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil || req.Container == "" {
			return nil, bad("container required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), guest.TimeoutFor(guest.MethodDockerInspect, 0))
		defer cancel()
		doc, err := dockerInspect(ctx, req.Container)
		return doc, asRPC(err)
	case guest.MethodDockerLogs:
		var req guest.DockerLogsRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil || req.Container == "" {
			return nil, bad("container required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), guest.TimeoutFor(guest.MethodDockerLogs, 0))
		defer cancel()
		out, err := dockerLogs(ctx, req)
		return out, asRPC(err)
	case guest.MethodDockerCompose:
		var req guest.DockerComposeRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil || req.Project == "" {
			return nil, bad("project required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), guest.TimeoutFor(guest.MethodDockerCompose, 0))
		defer cancel()
		out, err := dockerCompose(ctx, req.Project)
		return out, asRPC(err)
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
