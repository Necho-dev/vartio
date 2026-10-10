package guest

import "strings"

const (
	PathPrefix    = "/v1/probe"
	PathConnect   = "/v1/probe/connect"
	PathSessions  = "/v1/probe/sessions"
	PathForwards  = "/v1/probe/forwards"
	PathVMs       = "/v1/probe/vms"
	PathTermWS    = "/v1/probe/ws/term"
	PathDisplayWS = "/v1/probe/ws/display"
	// Agent-facing release endpoints authenticate with the guest token, not the admin token.
	PathAgent         = "/v1/probe/agent"
	PathAgentLatest   = PathAgent + "/latest"
	PathAgentDownload = PathAgent + "/download"
	PathReleases      = "/v1/probe/releases"
	PathUpgrade       = "/v1/probe/upgrade"
	PathHubKey        = "/v1/probe/hub/key"
)

func IsPublicProbePath(path string) bool {
	return path == PathConnect || strings.HasPrefix(path, PathPrefix+"/ws/") || strings.HasPrefix(path, PathAgent+"/")
}
