// vartio-release manages the Ed25519 key that signs vartio-agent builds.
//
//	vartio-release keygen            create the private key and add its public half to internal/release/release.pub
//	vartio-release sign <version dir> write <dir>/manifest.json with a signature per platform binary
//
// The private key lives outside the repo: $VARTIO_RELEASE_KEY, $PROBE_RELEASE_KEY,
// or ~/.config/probe-agent/release.key when that file is already there.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vartio/internal/release"
)

const pubFile = "internal/release/release.pub"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: vartio-release keygen | sign <dist/vartio-agent/VERSION>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen()
	case "sign":
		if len(os.Args) != 3 {
			err = errors.New("usage: vartio-release sign <dist/vartio-agent/VERSION>")
		} else {
			err = sign(os.Args[2])
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vartio-release:", err)
		os.Exit(1)
	}
}

func keyPath() string {
	if p := strings.TrimSpace(os.Getenv("VARTIO_RELEASE_KEY")); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("PROBE_RELEASE_KEY")); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	legacy := filepath.Join(home, ".config", "probe-agent", "release.key")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return filepath.Join(home, ".config", "vartio", "release.key")
}

func keygen() error {
	path := keyPath()
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", path)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	line := base64.StdEncoding.EncodeToString(pub)
	f, err := os.OpenFile(pubFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "# added %s\n%s\n", time.Now().Format("2006-01-02"), line); err != nil {
		return err
	}
	fmt.Printf("private key: %s (back it up; agents only accept builds it signs)\npublic key:  %s -> %s\n", path, line, pubFile)
	return nil
}

func loadKey() (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(keyPath())
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: not a release key", keyPath())
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// sign expects vartio-agent-<ver>-<os>-<arch>/vartio-agent[.exe]
// and still accepts the previous probe-agent directory layout.
func sign(dir string) error {
	key, err := loadKey()
	if err != nil {
		return err
	}
	ver := filepath.Base(filepath.Clean(dir))
	prefixes := []string{"vartio-agent-" + ver + "-", "probe-agent-" + ver + "-"}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	m := release.Manifest{Version: ver, Created: time.Now().UTC().Truncate(time.Second)}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		rest := ""
		matched := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(e.Name(), prefix) {
				rest = strings.TrimPrefix(e.Name(), prefix)
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		goos, arch, ok := strings.Cut(rest, "-")
		if !ok {
			continue
		}
		bin := ""
		for _, name := range []string{"vartio-agent", "probe-agent"} {
			candidate := name
			if goos == "windows" {
				candidate += ".exe"
			}
			if _, err := os.Stat(filepath.Join(dir, e.Name(), candidate)); err == nil {
				bin = candidate
				break
			}
		}
		if bin == "" {
			return fmt.Errorf("%s: no agent binary", e.Name())
		}
		rel := filepath.ToSlash(filepath.Join(e.Name(), bin))
		size, sum, err := digest(filepath.Join(dir, rel))
		if err != nil {
			return err
		}
		a := release.Artifact{OS: goos, Arch: arch, File: rel, Size: size, SHA256: sum}
		a.Sig = release.Sign(key, ver, a)
		m.Artifacts = append(m.Artifacts, a)
	}
	if len(m.Artifacts) == 0 {
		return fmt.Errorf("no vartio-agent-%s-*/ builds in %s", ver, dir)
	}
	sort.Slice(m.Artifacts, func(i, j int) bool { return m.Artifacts[i].File < m.Artifacts[j].File })
	keys := []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}
	for _, a := range m.Artifacts {
		if err := release.Verify(keys, ver, a, a.Size, a.SHA256); err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, release.ManifestName), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("  signed %d builds -> %s\n", len(m.Artifacts), filepath.Join(dir, release.ManifestName))
	return nil
}

func digest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}
