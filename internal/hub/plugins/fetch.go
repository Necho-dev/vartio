package plugins

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Fetcher puts a plugin binary at dest/vartio-plugin and returns that path.
type Fetcher interface {
	Fetch(ctx context.Context, item CatalogItem, dest string) (string, error)
}

type gitFetch struct{}

func (gitFetch) Fetch(ctx context.Context, item CatalogItem, dest string) (string, error) {
	if item.Git == "" {
		return "", fmt.Errorf("plugin %s has no repository", item.Name)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	src := filepath.Join(dest, "src")
	_ = os.RemoveAll(src)
	clone := exec.CommandContext(ctx, "git", "clone", "--depth", "1", item.Git, src)
	clone.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var errb bytes.Buffer
	clone.Stdout = &errb
	clone.Stderr = &errb
	if err := clone.Run(); err != nil {
		return "", fmt.Errorf("clone %s: %w\n%s", item.Git, err, tail(errb.String()))
	}
	bin, err := filepath.Abs(filepath.Join(dest, "vartio-plugin"))
	if err != nil {
		return "", err
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/vartio-plugin")
	build.Dir = src
	build.Env = append(os.Environ(), "GO111MODULE=on")
	errb.Reset()
	build.Stdout = &errb
	build.Stderr = &errb
	if err := build.Run(); err != nil {
		return "", fmt.Errorf("build %s: %w\n%s", item.Name, err, tail(errb.String()))
	}
	return bin, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return s[len(s)-2000:]
	}
	return s
}
