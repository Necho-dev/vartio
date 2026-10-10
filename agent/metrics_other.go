//go:build !windows

package main

import "github.com/shirou/gopsutil/v4/disk"

func skipMountOS(disk.PartitionStat) bool { return false }

func nonUnicastPkts(string) (rx, tx uint64) { return 0, 0 }
