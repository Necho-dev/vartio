package guest

import (
	"context"
	"encoding/json"
	"strings"

	"govmomi-wmks/internal/common"
)

func (h *Hub) Health(ctx context.Context, bios string) (Health, error) {
	raw, err := h.Call(ctx, bios, MethodHealth, nil)
	if err != nil {
		return Health{}, err
	}
	return decode[Health](raw)
}

func (h *Hub) Metrics(ctx context.Context, bios string) (Metrics, error) {
	raw, err := h.Call(ctx, bios, MethodMetrics, nil)
	if err != nil {
		return Metrics{}, err
	}
	return decode[Metrics](raw)
}

// DockerInspect returns a container's inspect document (Config.Env removed by the agent).
func (h *Hub) DockerInspect(ctx context.Context, key, container string) (json.RawMessage, error) {
	container = strings.TrimSpace(container)
	if container == "" {
		return nil, common.ErrBad("container required")
	}
	s, err := h.lookup(key)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, common.ErrOffline()
	}
	if !s.hello.Has(CapDockerInspect) {
		return nil, errCapMissing(CapDockerInspect)
	}
	return h.Call(ctx, key, MethodDockerInspect, DockerInspectRequest{Container: container})
}

func (h *Hub) dockerReady(key string) (*session, error) {
	s, err := h.lookup(key)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, common.ErrOffline()
	}
	if !s.hello.Has(CapDocker) {
		return nil, errCapMissing(CapDocker)
	}
	return s, nil
}

// DockerLogs returns a timestamped snapshot of a container's logs.
func (h *Hub) DockerLogs(ctx context.Context, key, container string, tail int, since, until int64) (DockerLogsResult, error) {
	container = strings.TrimSpace(container)
	if container == "" {
		return DockerLogsResult{}, common.ErrBad("container required")
	}
	if _, err := h.dockerReady(key); err != nil {
		return DockerLogsResult{}, err
	}
	raw, err := h.Call(ctx, key, MethodDockerLogs, DockerLogsRequest{Container: container, Tail: tail, Since: since, Until: until})
	if err != nil {
		return DockerLogsResult{}, err
	}
	return decode[DockerLogsResult](raw)
}

// DockerCompose returns the compose file for a project, if the agent can read it.
func (h *Hub) DockerCompose(ctx context.Context, key, project string) (DockerComposeResult, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return DockerComposeResult{}, common.ErrBad("project required")
	}
	if _, err := h.dockerReady(key); err != nil {
		return DockerComposeResult{}, err
	}
	raw, err := h.Call(ctx, key, MethodDockerCompose, DockerComposeRequest{Project: project})
	if err != nil {
		return DockerComposeResult{}, err
	}
	return decode[DockerComposeResult](raw)
}

func (h *Hub) Procs(ctx context.Context, bios string) ([]ProcInfo, error) {
	raw, err := h.Call(ctx, bios, MethodProcsList, nil)
	if err != nil {
		return nil, err
	}
	return decode[[]ProcInfo](raw)
}

func (h *Hub) ProcAction(ctx context.Context, bios, name, action string, confirm bool) (ActionResult, error) {
	if err := common.RequireConfirm(confirm); err != nil {
		return ActionResult{}, err
	}
	switch action {
	case "start", "stop", "kill":
	default:
		return ActionResult{}, common.ErrBad("action must be start, stop, or kill")
	}
	if name == "" {
		return ActionResult{}, common.ErrBad("name required")
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
	if err := common.RequireConfirm(confirm); err != nil {
		return ActionResult{}, err
	}
	switch action {
	case "start", "stop", "restart":
	default:
		return ActionResult{}, common.ErrBad("action must be start, stop, or restart")
	}
	if name == "" {
		return ActionResult{}, common.ErrBad("name required")
	}
	raw, err := h.Call(ctx, bios, MethodServicesAction, ActionRequest{Name: name, Action: action})
	if err != nil {
		return ActionResult{}, err
	}
	return decode[ActionResult](raw)
}

func (h *Hub) Exec(ctx context.Context, bios string, req ExecRequest) (ExecResult, error) {
	if req.Command == "" {
		return ExecResult{}, common.ErrBad("command required")
	}
	raw, err := h.Call(ctx, bios, MethodExec, req)
	if err != nil {
		return ExecResult{}, err
	}
	return decode[ExecResult](raw)
}

func (h *Hub) Power(ctx context.Context, bios, action string, confirm bool) (PowerResult, error) {
	if err := common.RequireConfirm(confirm); err != nil {
		return PowerResult{}, err
	}
	switch action {
	case "shutdown", "reboot":
	default:
		return PowerResult{}, common.ErrBad("action must be shutdown or reboot")
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
		return FilesGetResult{}, common.ErrBad("path required")
	}
	raw, err := h.Call(ctx, bios, MethodFilesGet, FilesGetRequest{Path: path, Stat: true})
	if err != nil {
		return FilesGetResult{}, err
	}
	return decode[FilesGetResult](raw)
}
