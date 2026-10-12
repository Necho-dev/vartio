package hub

import (
	"context"
	"encoding/json"
	"strings"

	"vartio/internal/common"
	"vartio/internal/entities"
)

func (h *Hub) Health(ctx context.Context, bios string) (entities.Health, error) {
	raw, err := h.Call(ctx, bios, entities.MethodHealth, nil)
	if err != nil {
		return entities.Health{}, err
	}
	return decode[entities.Health](raw)
}

func (h *Hub) Metrics(ctx context.Context, bios string) (entities.Metrics, error) {
	raw, err := h.Call(ctx, bios, entities.MethodMetrics, nil)
	if err != nil {
		return entities.Metrics{}, err
	}
	return decode[entities.Metrics](raw)
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
	if !s.hello.Has(entities.CapDockerInspect) {
		return nil, errCapMissing(entities.CapDockerInspect)
	}
	return h.Call(ctx, key, entities.MethodDockerInspect, entities.DockerInspectRequest{Container: container})
}

func (h *Hub) dockerReady(key string) (*session, error) {
	s, err := h.lookup(key)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, common.ErrOffline()
	}
	if !s.hello.Has(entities.CapDocker) {
		return nil, errCapMissing(entities.CapDocker)
	}
	return s, nil
}

// DockerLogs returns a timestamped snapshot of a container's logs.
func (h *Hub) DockerLogs(ctx context.Context, key, container string, tail int, since, until int64) (entities.DockerLogsResult, error) {
	container = strings.TrimSpace(container)
	if container == "" {
		return entities.DockerLogsResult{}, common.ErrBad("container required")
	}
	if _, err := h.dockerReady(key); err != nil {
		return entities.DockerLogsResult{}, err
	}
	raw, err := h.Call(ctx, key, entities.MethodDockerLogs, entities.DockerLogsRequest{Container: container, Tail: tail, Since: since, Until: until})
	if err != nil {
		return entities.DockerLogsResult{}, err
	}
	return decode[entities.DockerLogsResult](raw)
}

// DockerCompose returns the compose file for a project, if the agent can read it.
func (h *Hub) DockerCompose(ctx context.Context, key, project string) (entities.DockerComposeResult, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return entities.DockerComposeResult{}, common.ErrBad("project required")
	}
	if _, err := h.dockerReady(key); err != nil {
		return entities.DockerComposeResult{}, err
	}
	raw, err := h.Call(ctx, key, entities.MethodDockerCompose, entities.DockerComposeRequest{Project: project})
	if err != nil {
		return entities.DockerComposeResult{}, err
	}
	return decode[entities.DockerComposeResult](raw)
}

func (h *Hub) Apps(ctx context.Context, bios string) ([]entities.AppInfo, error) {
	raw, err := h.Call(ctx, bios, entities.MethodAppsList, nil)
	if err != nil {
		return nil, err
	}
	out, err := decode[[]entities.AppInfo](raw)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []entities.AppInfo{}
	}
	return out, nil
}

func (h *Hub) Ports(ctx context.Context, bios string) ([]entities.PortInfo, error) {
	raw, err := h.Call(ctx, bios, entities.MethodPortsList, nil)
	if err != nil {
		return nil, err
	}
	return decode[[]entities.PortInfo](raw)
}

func (h *Hub) Procs(ctx context.Context, bios string) ([]entities.ProcInfo, error) {
	raw, err := h.Call(ctx, bios, entities.MethodProcsList, nil)
	if err != nil {
		return nil, err
	}
	return decode[[]entities.ProcInfo](raw)
}

func (h *Hub) ProcAction(ctx context.Context, bios, name, action string, confirm bool) (entities.ActionResult, error) {
	if err := common.RequireConfirm(confirm); err != nil {
		return entities.ActionResult{}, err
	}
	switch action {
	case "start", "stop", "kill":
	default:
		return entities.ActionResult{}, common.ErrBad("action must be start, stop, or kill")
	}
	if name == "" {
		return entities.ActionResult{}, common.ErrBad("name required")
	}
	raw, err := h.Call(ctx, bios, entities.MethodProcsAction, entities.ActionRequest{Name: name, Action: action})
	if err != nil {
		return entities.ActionResult{}, err
	}
	return decode[entities.ActionResult](raw)
}

func (h *Hub) Services(ctx context.Context, bios string) ([]entities.ServiceInfo, error) {
	raw, err := h.Call(ctx, bios, entities.MethodServicesList, nil)
	if err != nil {
		return nil, err
	}
	return decode[[]entities.ServiceInfo](raw)
}

func (h *Hub) ServiceAction(ctx context.Context, bios, name, action string, confirm bool) (entities.ActionResult, error) {
	if err := common.RequireConfirm(confirm); err != nil {
		return entities.ActionResult{}, err
	}
	switch action {
	case "start", "stop", "restart":
	default:
		return entities.ActionResult{}, common.ErrBad("action must be start, stop, or restart")
	}
	if name == "" {
		return entities.ActionResult{}, common.ErrBad("name required")
	}
	raw, err := h.Call(ctx, bios, entities.MethodServicesAction, entities.ActionRequest{Name: name, Action: action})
	if err != nil {
		return entities.ActionResult{}, err
	}
	return decode[entities.ActionResult](raw)
}

func (h *Hub) Exec(ctx context.Context, bios string, req entities.ExecRequest) (entities.ExecResult, error) {
	if req.Command == "" {
		return entities.ExecResult{}, common.ErrBad("command required")
	}
	raw, err := h.Call(ctx, bios, entities.MethodExec, req)
	if err != nil {
		return entities.ExecResult{}, err
	}
	return decode[entities.ExecResult](raw)
}

func (h *Hub) Power(ctx context.Context, bios, action string, confirm bool) (entities.PowerResult, error) {
	if err := common.RequireConfirm(confirm); err != nil {
		return entities.PowerResult{}, err
	}
	switch action {
	case "shutdown", "reboot":
	default:
		return entities.PowerResult{}, common.ErrBad("action must be shutdown or reboot")
	}
	raw, err := h.Call(ctx, bios, entities.MethodPower, entities.PowerRequest{Action: action})
	if err != nil {
		return entities.PowerResult{}, err
	}
	return decode[entities.PowerResult](raw)
}

func (h *Hub) FilesList(ctx context.Context, bios, path string) (entities.FilesListResult, error) {
	raw, err := h.Call(ctx, bios, entities.MethodFilesList, entities.FilesListRequest{Path: path})
	if err != nil {
		return entities.FilesListResult{}, err
	}
	out, err := decode[entities.FilesListResult](raw)
	if err != nil {
		return entities.FilesListResult{}, err
	}
	if out.Entries == nil {
		out.Entries = []entities.FileEntry{}
	}
	return out, nil
}

func (h *Hub) FileStat(ctx context.Context, bios, path string) (entities.FilesGetResult, error) {
	if path == "" {
		return entities.FilesGetResult{}, common.ErrBad("path required")
	}
	raw, err := h.Call(ctx, bios, entities.MethodFilesGet, entities.FilesGetRequest{Path: path, Stat: true})
	if err != nil {
		return entities.FilesGetResult{}, err
	}
	return decode[entities.FilesGetResult](raw)
}
