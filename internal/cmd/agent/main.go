// probe-agent dials a hub and serves the reverse RPC. It has no inbound port.
package main

import "vartio/agent"

func main() {
	agent.Run()
}
