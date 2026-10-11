// Package sdk is the public contract a Vartio plugin binary uses.
// Plugins import this module. They do not import the hub.
package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// PluginName is the route name: a short lowercase token.
var PluginName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

const (
	HostAPI       = "1"
	ManifestV     = 1
	PackageSuffix = ".vtp"
	BinaryName    = "vartio-plugin"
	ManifestName  = "manifest.json"
	ChecksumName  = "checksums.json"
	SignatureName = "signature.json"
)

const (
	CapTunnelTCP    = "tunnel.tcp"
	CapFsWhich      = "fs.which"
	CapFsRead       = "fs.read"
	CapProcSpawn    = "proc.spawn"
	CapNetListeners = "net.listeners"
)

// Machines a plugin may declare. mac is the Hello os value darwin.
var knownMachines = map[string]struct{}{
	"mac": {}, "windows": {}, "linux": {}, "docker": {}, "vmware": {},
}

var knownPrimitives = map[string]struct{}{
	CapTunnelTCP: {}, CapFsWhich: {}, CapFsRead: {}, CapProcSpawn: {}, CapNetListeners: {},
}

// Manifest is the identity contract. The hub reads it before it executes the binary.
type Manifest struct {
	ManifestVersion int      `json:"manifestVersion"`
	Name            string   `json:"name"`
	Title           string   `json:"title"`
	Version         string   `json:"version"`
	Description     string   `json:"description,omitempty"`
	Source          string   `json:"source,omitempty"`
	Engines         Engines  `json:"engines"`
	Primitives      []string `json:"primitives"`
	Targets         []string `json:"targets,omitempty"`
	Machines        []string `json:"machines"`
	UI              bool     `json:"ui"`
}

type Engines struct {
	HostAPI string `json:"hostApi"`
}

// ParseManifest checks the contract and returns a normalized copy.
func ParseManifest(raw []byte) (Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("manifest: trailing data")
		}
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	m.Name = strings.TrimSpace(m.Name)
	m.Title = strings.TrimSpace(m.Title)
	m.Version = strings.TrimSpace(m.Version)
	m.Engines.HostAPI = strings.TrimSpace(m.Engines.HostAPI)
	if m.ManifestVersion != ManifestV {
		return Manifest{}, fmt.Errorf("manifest: manifestVersion 需要是 %d", ManifestV)
	}
	if !PluginName.MatchString(m.Name) {
		return Manifest{}, fmt.Errorf("manifest: 插件名 %q 不合法", m.Name)
	}
	if m.Title == "" || m.Version == "" {
		return Manifest{}, fmt.Errorf("manifest: 需要 title 和 version")
	}
	if m.Engines.HostAPI != HostAPI {
		return Manifest{}, fmt.Errorf("manifest: hostApi 需要是 %s", HostAPI)
	}
	if len(m.Machines) == 0 {
		return Manifest{}, fmt.Errorf("manifest: 需要 machines")
	}
	seen := map[string]bool{}
	for _, machine := range m.Machines {
		if _, ok := knownMachines[machine]; !ok || seen[machine] {
			return Manifest{}, fmt.Errorf("manifest: 不认识的机器 %q", machine)
		}
		seen[machine] = true
	}
	if m.Primitives == nil {
		m.Primitives = []string{}
	}
	for _, c := range m.Primitives {
		if _, ok := knownPrimitives[c]; !ok {
			return Manifest{}, fmt.Errorf("manifest: 不认识的原语 %q", c)
		}
	}
	if m.Targets == nil {
		m.Targets = []string{}
	}
	return m, nil
}

// SameManifest reports whether two parsed manifests describe the same contract.
func SameManifest(a, b Manifest) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ab, bb)
}

// AgentLabels are the machine tags a probe carries.
// mac stands for os=darwin. docker and vmware come from capabilities,
// not from the guest firmware virt field.
func AgentLabels(os string, caps []string) []string {
	var out []string
	switch os {
	case "darwin":
		out = append(out, "mac")
	case "windows", "linux":
		out = append(out, os)
	}
	for _, c := range caps {
		if c == "docker" || c == "vmware" {
			out = append(out, c)
		}
	}
	return out
}

// MachineMatch is true when the agent carries any machine the plugin declared.
func MachineMatch(machines []string, os string, caps []string) bool {
	have := AgentLabels(os, caps)
	for _, m := range machines {
		for _, h := range have {
			if m == h {
				return true
			}
		}
	}
	return false
}
