package guest

import (
	"context"

	"govmomi-wmks/internal/esxi"
)

func (h *Hub) Health(ctx context.Context, bios string) (Health, error) {
	raw, err := h.Call(ctx, bios, MethodHealth, nil)
	if err != nil {
		return Health{}, err
	}
	return decode[Health](raw)
}

func (h *Hub) Procs(ctx context.Context, bios string) ([]ProcInfo, error) {
	raw, err := h.Call(ctx, bios, MethodProcsList, nil)
	if err != nil {
		return nil, err
	}
	return decode[[]ProcInfo](raw)
}

func (h *Hub) ProcAction(ctx context.Context, bios, name, action string, confirm bool) (ActionResult, error) {
	if err := esxi.RequireConfirm(confirm); err != nil {
		return ActionResult{}, err
	}
	switch action {
	case "start", "stop", "kill":
	default:
		return ActionResult{}, esxi.ErrBad("action must be start, stop, or kill")
	}
	if name == "" {
		return ActionResult{}, esxi.ErrBad("name required")
	}
	raw, err := h.Call(ctx, bios, MethodProcsAction, ActionRequest{Name: name, Action: action})
	if err != nil {
		return ActionResult{}, err
	}
	return decode[ActionResult](raw)
}

func (h *Hub) Services(ctx context.Context, bios string) ([]ServiceInfo, error) {
	raw, err := h.Call(ctx, bios, MethodServicesList, nil)
	if err != nil {
		return nil, err
	}
	return decode[[]ServiceInfo](raw)
}

func (h *Hub) ServiceAction(ctx context.Context, bios, name, action string, confirm bool) (ActionResult, error) {
	if err := esxi.RequireConfirm(confirm); err != nil {
		return ActionResult{}, err
	}
	switch action {
	case "start", "stop", "restart":
	default:
		return ActionResult{}, esxi.ErrBad("action must be start, stop, or restart")
	}
	if name == "" {
		return ActionResult{}, esxi.ErrBad("name required")
	}
	raw, err := h.Call(ctx, bios, MethodServicesAction, ActionRequest{Name: name, Action: action})
	if err != nil {
		return ActionResult{}, err
	}
	return decode[ActionResult](raw)
}

func (h *Hub) Exec(ctx context.Context, bios string, req ExecRequest) (ExecResult, error) {
	if req.Command == "" {
		return ExecResult{}, esxi.ErrBad("command required")
	}
	raw, err := h.Call(ctx, bios, MethodExec, req)
	if err != nil {
		return ExecResult{}, err
	}
	return decode[ExecResult](raw)
}

func (h *Hub) Power(ctx context.Context, bios, action string, confirm bool) (PowerResult, error) {
	if err := esxi.RequireConfirm(confirm); err != nil {
		return PowerResult{}, err
	}
	switch action {
	case "shutdown", "reboot":
	default:
		return PowerResult{}, esxi.ErrBad("action must be shutdown or reboot")
	}
	raw, err := h.Call(ctx, bios, MethodPower, PowerRequest{Action: action})
	if err != nil {
		return PowerResult{}, err
	}
	return decode[PowerResult](raw)
}

func (h *Hub) FilesList(ctx context.Context, bios, path string) (FilesListResult, error) {
	raw, err := h.Call(ctx, bios, MethodFilesList, FilesListRequest{Path: path})
	if err != nil {
		return FilesListResult{}, err
	}
	out, err := decode[FilesListResult](raw)
	if err != nil {
		return FilesListResult{}, err
	}
	if out.Entries == nil {
		out.Entries = []FileEntry{}
	}
	return out, nil
}

func (h *Hub) FileStat(ctx context.Context, bios, path string) (FilesGetResult, error) {
	if path == "" {
		return FilesGetResult{}, esxi.ErrBad("path required")
	}
	raw, err := h.Call(ctx, bios, MethodFilesGet, FilesGetRequest{Path: path, Stat: true})
	if err != nil {
		return FilesGetResult{}, err
	}
	return decode[FilesGetResult](raw)
}
