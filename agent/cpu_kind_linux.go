//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
)

func coreKinds(n int) []string {
	caps := make([]int, n)
	for i := 0; i < n; i++ {
		b, err := os.ReadFile("/sys/devices/system/cpu/cpu" + strconv.Itoa(i) + "/cpu_capacity")
		if err != nil {
			return nil
		}
		c, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return nil
		}
		caps[i] = c
	}
	return capacityKinds(caps)
}
