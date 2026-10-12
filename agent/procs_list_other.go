//go:build !darwin

package agent

import "vartio/internal/entities"

func listProcs() ([]entities.ProcInfo, error) {
	return listProcsGopsutil()
}
