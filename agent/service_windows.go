//go:build windows

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// serviceName stays probe-agent so an upgraded Windows service is the same service.
const serviceName = "probe-agent"

func runServiceCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: vartio-agent service install|remove|run")
		return 2
	}
	switch args[0] {
	case "install":
		if err := installService(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("installed", serviceName)
		return 0
	case "remove":
		if err := removeService(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("removed", serviceName)
		return 0
	case "run":
		return runAsService()
	default:
		fmt.Fprintln(os.Stderr, "usage: vartio-agent service install|remove|run")
		return 2
	}
}

func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	if autostartRegistered() {
		return fmt.Errorf("已设置为登录自启的托盘模式。托盘模式与服务二选一，请先执行：probe-agent.exe uninstall")
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("%s already installed", serviceName)
	}
	s, err = m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: "Vartio Agent",
		StartType:   mgr.StartAutomatic,
		Description: "Outbound guest probe for probe-hub / vm-server",
	}, "service", "run")
	if err != nil {
		return err
	}
	defer s.Close()
	_ = eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info)
	return nil
}

func removeService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	return s.Delete()
}

type windowsService struct{}

func (windowsService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	cfg := loadConfig()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, cfg) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				return false, 0
			}
		case <-errc:
			return true, 1
		}
	}
}

func runAsService() int {
	if err := svc.Run(serviceName, windowsService{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
