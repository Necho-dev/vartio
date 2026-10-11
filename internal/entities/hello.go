package entities

const (
	HeaderDevice     = "X-Guest-Device"
	HeaderCredential = "X-Guest-Credential"
)

// HelloSigMessage is what the hub signs in HelloOK: the agent's nonce, the device id
// and the credential being handed out, so none of them can be swapped in transit.
func HelloSigMessage(nonce, device, credential string) []byte {
	return []byte("probe-hub-hello/v1\n" + nonce + "\n" + device + "\n" + credential)
}
