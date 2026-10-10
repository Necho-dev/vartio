// Package release describes signed probe-agent builds: the manifest the dist
// script writes next to each version, and how agents verify a download.
package release

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ManifestName sits in dist/probe-agent/<version>/.
const ManifestName = "manifest.json"

type Manifest struct {
	Version   string     `json:"version"`
	Created   time.Time  `json:"created"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one platform's raw agent binary. File is relative to the version directory.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	File   string `json:"file"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Sig    string `json:"sig"` // base64 Ed25519 over Message
}

func (m *Manifest) Find(goos, arch string) *Artifact {
	for i := range m.Artifacts {
		if m.Artifacts[i].OS == goos && m.Artifacts[i].Arch == arch {
			return &m.Artifacts[i]
		}
	}
	return nil
}

// Message binds the digest to version and platform, so a valid signature cannot
// be replayed for another platform or to pass an old build off as a new one.
func Message(version string, a Artifact) []byte {
	return fmt.Appendf(nil, "probe-agent|%s|%s|%s|%d|%s", version, a.OS, a.Arch, a.Size, strings.ToLower(a.SHA256))
}

func Sign(key ed25519.PrivateKey, version string, a Artifact) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, Message(version, a)))
}

//go:embed release.pub
var trustedPub string

var ErrUnsigned = errors.New("artifact is not signed")

// TrustedKeys are the release public keys compiled into the agent (one per line).
func TrustedKeys() ([]ed25519.PublicKey, error) {
	return ParseKeys(trustedPub)
}

func ParseKeys(text string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(line)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("bad release public key %q", line)
		}
		keys = append(keys, ed25519.PublicKey(b))
	}
	return keys, nil
}

// Verify checks the artifact's signature against any trusted key; sum is the
// hex SHA-256 the caller computed over the downloaded bytes.
func Verify(keys []ed25519.PublicKey, version string, a Artifact, size int64, sum string) error {
	if size != a.Size || !strings.EqualFold(sum, a.SHA256) {
		return fmt.Errorf("checksum mismatch: got %d bytes sha256 %s, want %d bytes %s", size, sum, a.Size, a.SHA256)
	}
	if _, err := hex.DecodeString(a.SHA256); err != nil || len(a.SHA256) != 64 {
		return errors.New("manifest sha256 malformed")
	}
	if a.Sig == "" {
		return ErrUnsigned
	}
	sig, err := base64.StdEncoding.DecodeString(a.Sig)
	if err != nil {
		return errors.New("signature malformed")
	}
	if len(keys) == 0 {
		return errors.New("no trusted release key compiled in")
	}
	msg := Message(version, a)
	for _, k := range keys {
		if ed25519.Verify(k, msg, sig) {
			return nil
		}
	}
	return errors.New("signature does not match any trusted release key")
}

// Compare orders dotted numeric versions ("1.10.0" > "1.9.2"); a missing part counts as 0.
func Compare(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
