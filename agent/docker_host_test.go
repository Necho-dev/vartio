package agent

import (
	"runtime"
	"testing"
)

func TestDarwinSkipsDocker(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip()
	}
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	if docker() != nil {
		t.Fatal("darwin host agent attached to a docker engine")
	}
}
