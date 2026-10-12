package agent

import (
	"context"
	"encoding/json"
	"errors"

	"vartio/internal/entities"
)

func (a *agent) dispatch(fr entities.Frame) (any, *entities.FrameError) {
	switch fr.Method {
	case entities.MethodHealth:
		h, err := collectHealth(a.cfg.Watches)
		return h, asRPC(err)
	case entities.MethodMetrics:
		return collectMetrics(), nil
	case entities.MethodUpgrade:
		var req entities.UpgradeRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return upgrade(context.Background(), a.cfg, req, func(st entities.UpgradeStep) { a.streamUpgrade(fr.ID, st) }), nil
	case entities.MethodProcsList:
		list, err := listProcs()
		return list, asRPC(err)
	case entities.MethodPortsList:
		list, err := listPorts()
		return list, asRPC(err)
	case entities.MethodAppsList:
		list, err := listApps()
		return list, asRPC(err)
	case entities.MethodProcsAction:
		var req entities.ActionRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return procAction(req)
	case entities.MethodServicesList:
		list, err := listServices()
		return list, asRPC(err)
	case entities.MethodServicesAction:
		var req entities.ActionRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return serviceAction(req)
	case entities.MethodExec:
		var req entities.ExecRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return runExec(req), nil
	case entities.MethodTermOpen:
		var req entities.TermOpenRequest
		_ = json.Unmarshal(fr.Params, &req)
		id := fr.ID
		if err := a.term.open(id, req, func(p []byte) { a.streamOut(id, p) }, func() { a.streamExit(id) }); err != nil {
			return nil, asRPC(err)
		}
		return entities.TermOpenResult{OK: true}, nil
	case entities.MethodTermResize:
		var req entities.TermOpenRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		a.term.setSize(req.ID, req.Cols, req.Rows)
		return map[string]bool{"ok": true}, nil
	case entities.MethodTermClose:
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(fr.Params, &req)
		a.term.close(req.ID)
		return map[string]bool{"ok": true}, nil
	case entities.MethodPower:
		var req entities.PowerRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return doPower(req.Action)
	case entities.MethodDisplayOpen:
		var req entities.DisplayOpenRequest
		_ = json.Unmarshal(fr.Params, &req)
		res, err := a.display.open(fr.ID, req, func(p []byte) { a.streamDisplay(fr.ID, p) })
		if err != nil {
			return nil, err
		}
		return res, nil
	case entities.MethodDisplaySelect:
		var req entities.DisplayOpenRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		if err := a.display.selectScreen(req.ID, req.Screen); err != nil {
			return nil, err
		}
		return map[string]int{"screen": req.Screen}, nil
	case entities.MethodDisplayQuality:
		var req entities.DisplayOpenRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return map[string]string{"quality": a.display.setQuality(req.ID, req.Quality)}, nil
	case entities.MethodDisplayClose:
		var req entities.DisplayOpenRequest
		_ = json.Unmarshal(fr.Params, &req)
		a.display.close(req.ID)
		return map[string]bool{"ok": true}, nil
	case entities.MethodFilesList:
		return filesList(fr.Params)
	case entities.MethodFilesGet:
		return filesGet(fr.Params)
	case entities.MethodFilesPut:
		return filesPut(fr.Params)
	case entities.MethodFilesOp:
		return filesOp(fr.Params)
	case entities.MethodTrash:
		return trashOp(fr.Params)
	case entities.MethodTunnelOpen:
		return a.tunnelOpen(fr.Params)
	case entities.MethodFsWhich:
		var req entities.WhichRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return which(req), nil
	case entities.MethodFsRead:
		var req entities.ReadRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return readFiles(req), nil
	case entities.MethodProcSpawn:
		var req entities.SpawnRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil {
			return nil, bad("invalid params")
		}
		return spawnProc(req)
	case entities.MethodNetListeners:
		var req entities.ListenersRequest
		_ = json.Unmarshal(fr.Params, &req)
		ctx, cancel := context.WithTimeout(context.Background(), entities.TimeoutFor(entities.MethodNetListeners, 0))
		defer cancel()
		list, err := listeners(ctx, req)
		return list, asRPC(err)
	case entities.MethodDockerInspect:
		var req entities.DockerInspectRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil || req.Container == "" {
			return nil, bad("container required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), entities.TimeoutFor(entities.MethodDockerInspect, 0))
		defer cancel()
		doc, err := dockerInspect(ctx, req.Container)
		return doc, asRPC(err)
	case entities.MethodDockerLogs:
		var req entities.DockerLogsRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil || req.Container == "" {
			return nil, bad("container required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), entities.TimeoutFor(entities.MethodDockerLogs, 0))
		defer cancel()
		out, err := dockerLogs(ctx, req)
		return out, asRPC(err)
	case entities.MethodDockerCompose:
		var req entities.DockerComposeRequest
		if err := json.Unmarshal(fr.Params, &req); err != nil || req.Project == "" {
			return nil, bad("project required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), entities.TimeoutFor(entities.MethodDockerCompose, 0))
		defer cancel()
		out, err := dockerCompose(ctx, req.Project)
		return out, asRPC(err)
	default:
		return nil, &entities.FrameError{Code: "not_implemented", Message: fr.Method}
	}
}

func asRPC(err error) *entities.FrameError {
	if err == nil {
		return nil
	}
	var fe *entities.FrameError
	if errors.As(err, &fe) {
		return fe
	}
	return &entities.FrameError{Code: "error", Message: err.Error()}
}

func bad(msg string) *entities.FrameError {
	return &entities.FrameError{Code: "bad_request", Message: msg}
}
