package sdk

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

const maxFile = 256 << 20

// Package is an unpacked .vtp. Verify checks the signature and the file hashes.
type Package struct {
	ManifestRaw []byte
	Binary      []byte
	Checksums   []byte
	Signature   Signature
}

// Pack builds a .vtp from the manifest and plugin binary.
func Pack(manifest, binary []byte, key Key) ([]byte, error) {
	if _, err := ParseManifest(manifest); err != nil {
		return nil, err
	}
	if len(binary) == 0 {
		return nil, fmt.Errorf("package: empty binary")
	}
	sums, err := CanonicalChecksums(manifest, binary)
	if err != nil {
		return nil, err
	}
	sig, err := SignChecksums(key, sums)
	if err != nil {
		return nil, err
	}
	sigRaw, err := json.Marshal(sig)
	if err != nil {
		return nil, err
	}
	sigRaw = append(sigRaw, '\n')

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct {
		name string
		body []byte
	}{
		{ManifestName, manifest},
		{BinaryName, binary},
		{ChecksumName, sums},
		{SignatureName, sigRaw},
	}
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		h.SetModTime(time.Unix(0, 0).UTC())
		h.SetMode(0o644)
		if f.name == BinaryName {
			h.SetMode(0o755)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Open reads a .vtp. It rejects any path other than the four package files.
func Open(data []byte) (Package, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Package{}, fmt.Errorf("package: %w", err)
	}
	if len(zr.File) != 4 {
		return Package{}, fmt.Errorf("package: want 4 files, got %d", len(zr.File))
	}
	got := map[string][]byte{}
	for _, f := range zr.File {
		name := f.Name
		if name != ManifestName && name != BinaryName && name != ChecksumName && name != SignatureName {
			return Package{}, fmt.Errorf("package: unexpected file %q", name)
		}
		if f.UncompressedSize64 > maxFile {
			return Package{}, fmt.Errorf("package: %s is too large", name)
		}
		rc, err := f.Open()
		if err != nil {
			return Package{}, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxFile+1))
		rc.Close()
		if err != nil {
			return Package{}, err
		}
		if len(b) > maxFile {
			return Package{}, fmt.Errorf("package: %s is too large", name)
		}
		if _, ok := got[name]; ok {
			return Package{}, fmt.Errorf("package: duplicate %s", name)
		}
		got[name] = b
	}
	var sig Signature
	if err := json.Unmarshal(got[SignatureName], &sig); err != nil {
		return Package{}, fmt.Errorf("signature: %w", err)
	}
	return Package{
		ManifestRaw: got[ManifestName],
		Binary:      got[BinaryName],
		Checksums:   got[ChecksumName],
		Signature:   sig,
	}, nil
}

// Verify checks the Ed25519 signature and returns the parsed manifest.
func (p Package) Verify(pub ed25519.PublicKey, keyID string) (Manifest, error) {
	if err := VerifyChecksums(pub, keyID, p.Checksums, p.ManifestRaw, p.Binary, p.Signature); err != nil {
		return Manifest{}, err
	}
	return ParseManifest(p.ManifestRaw)
}

// WriteTree writes the verified payload into dir.
func (p Package) WriteTree(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sig, err := json.Marshal(p.Signature)
	if err != nil {
		return err
	}
	files := []struct {
		name string
		body []byte
		mode os.FileMode
	}{
		{ManifestName, p.ManifestRaw, 0o644},
		{BinaryName, p.Binary, 0o755},
		{ChecksumName, p.Checksums, 0o644},
		{SignatureName, append(sig, '\n'), 0o644},
	}
	for _, f := range files {
		if err := os.WriteFile(dir+"/"+f.name, f.body, f.mode); err != nil {
			return err
		}
	}
	return nil
}
