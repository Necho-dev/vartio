//go:build windows

package agent

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"vartio/internal/entities"
)

func listServices() ([]entities.ServiceInfo, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	defer m.Disconnect()
	names, err := m.ListServices()
	if err != nil {
		return nil, err
	}
	out := make([]entities.ServiceInfo, 0, len(names))
	for _, name := range names {
		s, err := m.OpenService(name)
		if err != nil {
			continue
		}
		st, err := s.Query()
		s.Close()
		if err != nil {
			continue
		}
		out = append(out, entities.ServiceInfo{Name: name, Status: svcState(st.State)})
	}
	return out, nil
}

func serviceAlive(name string) (bool, string) {
	m, err := mgr.Connect()
	if err != nil {
		return false, err.Error()
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return false, err.Error()
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return false, err.Error()
	}
	return st.State == svc.Running, svcState(st.State)
}

func serviceAction(req entities.ActionRequest) (entities.ActionResult, *entities.FrameError) {
	m, err := mgr.Connect()
	if err != nil {
		return entities.ActionResult{}, asRPC(err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(req.Name)
	if err != nil {
		return entities.ActionResult{}, asRPC(err)
	}
	defer s.Close()
	switch req.Action {
	case "start":
		if err := s.Start(); err != nil {
			return entities.ActionResult{}, asRPC(err)
		}
	case "stop":
		if _, err := s.Control(svc.Stop); err != nil {
			return entities.ActionResult{}, asRPC(err)
		}
	case "restart":
		_, _ = s.Control(svc.Stop)
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			st, err := s.Query()
			if err == nil && st.State == svc.Stopped {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err := s.Start(); err != nil {
			return entities.ActionResult{}, asRPC(err)
		}
	default:
		return entities.ActionResult{}, bad("action must be start, stop, or restart")
	}
	return entities.ActionResult{OK: true, Name: req.Name, Action: req.Action}, nil
}

func svcState(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start_pending"
	case svc.StopPending:
		return "stop_pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue_pending"
	case svc.PausePending:
		return "pause_pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("%d", s)
	}
}
