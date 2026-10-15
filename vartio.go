// Package vartio names the Hub-Agent module. Binaries are vartio and vartio-agent.
package vartio

// AppName is the product name shared by the hub and the agent.
const AppName = "vartio"

// Version is the agent build stamped into Hello and dist packages.
// Release builds may override it with -ldflags -X vartio.Version=...
var Version = "1.28.8"
