package guest

import "strings"

const (
	PathPrefix    = "/v1/probe"
	PathConnect   = "/v1/probe/connect"
	PathSessions  = "/v1/probe/sessions"
	PathVMs       = "/v1/probe/vms"
	PathTermWS    = "/v1/probe/ws/term"
	PathDisplayWS = "/v1/probe/ws/display"
)

func IsPublicProbePath(path string) bool {
	return path == PathConnect || strings.HasPrefix(path, PathPrefix+"/ws/")
}
