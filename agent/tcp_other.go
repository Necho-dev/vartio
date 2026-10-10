//go:build !linux && !darwin && !windows

package main

import (
	"context"
	"errors"

	"govmomi-wmks/internal/guest"
)

func tcpStats(context.Context) (*guest.TCPStats, error) {
	return nil, errors.New("tcp stats not supported on this platform")
}
