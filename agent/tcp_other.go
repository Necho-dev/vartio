//go:build !linux && !darwin && !windows

package agent

import (
	"context"
	"errors"

	"vartio/internal/entities"
)

func tcpStats(context.Context) (*entities.TCPStats, error) {
	return nil, errors.New("tcp stats not supported on this platform")
}
