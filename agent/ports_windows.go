//go:build windows

package agent

import (
	"golang.org/x/sys/windows"

	"vartio/internal/entities"
)

func listPorts() ([]entities.PortInfo, error) {
	names := procNames()
	var out []entities.PortInfo
	for _, t := range []struct {
		af  uint32
		tcp func([]byte) []entities.PortInfo
		udp func([]byte) []entities.PortInfo
	}{
		{windows.AF_INET, parseWinTCP4, parseWinUDP4},
		{windows.AF_INET6, parseWinTCP6, parseWinUDP6},
	} {
		if b, err := extTable(procExtTCPTable, t.af, tcpTableOwnerPIDAll); err == nil {
			out = append(out, t.tcp(b)...)
		}
		if b, err := extTable(procExtUDPTable, t.af, udpTableOwnerPID); err == nil {
			out = append(out, t.udp(b)...)
		}
	}
	for i := range out {
		if out[i].PID > 0 {
			out[i].Name = names[out[i].PID]
		}
	}
	return dedupePorts(out), nil
}
