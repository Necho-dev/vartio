package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"

	"govmomi-wmks/internal/guest"
)

func runExec(req guest.ExecRequest) guest.ExecResult {
	timeout := guest.TimeoutFor(guest.MethodExec, req.TimeoutSec)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	shell := req.Shell
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "cmd"
		} else {
			shell = "sh"
		}
	}
	switch shell {
	case "ps", "powershell":
		args := append([]string{"-NoProfile", "-Command", req.Command}, req.Args...)
		cmd = exec.CommandContext(ctx, powershellName(), args...)
	case "cmd":
		args := append([]string{"/c", req.Command}, req.Args...)
		cmd = exec.CommandContext(ctx, "cmd.exe", args...)
	default:
		if len(req.Args) > 0 {
			name, args := hostCommand(req.Command, req.Args...)
			cmd = exec.CommandContext(ctx, name, args...)
		} else if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd.exe", "/c", req.Command)
		} else {
			name, args := hostCommand("/bin/sh", "-c", req.Command)
			cmd = exec.CommandContext(ctx, name, args...)
		}
	}
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := guest.ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		out.TimedOut = true
		out.ExitCode = -1
		return out
	}
	if err == nil {
		return out
	}
	if ee, ok := err.(*exec.ExitError); ok {
		out.ExitCode = ee.ExitCode()
		return out
	}
	out.ExitCode = -1
	if out.Stderr == "" {
		out.Stderr = err.Error()
	}
	return out
}

func powershellName() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	return "pwsh"
}
