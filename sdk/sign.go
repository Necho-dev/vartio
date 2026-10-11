package sdk

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Checksums binds the two payload files. The signature covers these exact bytes.
type Checksums struct {
	Manifest string `json:"manifest.json"`
	Binary   string `json:"vartio-plugin"`
}

// Signature is the Ed25519 signature of checksums.json.
type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"keyId"`
	Value     string `json:"value"`
}

// Key is an Ed25519 signing key. Seed is the 32-byte private seed.
type Key struct {
	ID   string
	Seed []byte
}

func (k Key) Private() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(k.Seed)
}

func (k Key) Public() ed25519.PublicKey {
	return k.Private().Public().(ed25519.PublicKey)
}

func (k Key) PublicString() string {
	return base64.StdEncoding.EncodeToString(k.Public())
}

// GenerateKey creates a new signing key.
func GenerateKey(id string) (Key, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Key{}, fmt.Errorf("key id required")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Key{}, err
	}
	return Key{ID: id, Seed: priv.Seed()}, nil
}

// WriteKey stores the id and base64 seed. The file mode is 0600.
func WriteKey(path string, k Key) error {
	body := k.ID + "\n" + base64.StdEncoding.EncodeToString(k.Seed) + "\n"
	return os.WriteFile(path, []byte(body), 0o600)
}

// ReadKey loads a key written by WriteKey.
func ReadKey(path string) (Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Key{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		return Key{}, fmt.Errorf("%s: expected key id and seed", path)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil {
		return Key{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(seed) != ed25519.SeedSize {
		return Key{}, fmt.Errorf("%s: seed must be %d bytes", path, ed25519.SeedSize)
	}
	return Key{ID: strings.TrimSpace(lines[0]), Seed: seed}, nil
}

// ParsePublicKey decodes a base64 raw Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key: want %d bytes", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CanonicalChecksums is the exact byte string that gets signed.
func CanonicalChecksums(manifest, binary []byte) ([]byte, error) {
	body, err := json.Marshal(Checksums{Manifest: hashHex(manifest), Binary: hashHex(binary)})
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	return body, nil
}

// SignChecksums signs the canonical checksum document.
func SignChecksums(k Key, checksums []byte) (Signature, error) {
	if k.ID == "" || len(k.Seed) != ed25519.SeedSize {
		return Signature{}, fmt.Errorf("signing key is incomplete")
	}
	sig := ed25519.Sign(k.Private(), checksums)
	return Signature{
		Algorithm: "ed25519",
		KeyID:     k.ID,
		Value:     base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// VerifyChecksums checks the signature and that the files match checksums.json.
func VerifyChecksums(pub ed25519.PublicKey, keyID string, checksums, manifest, binary []byte, sig Signature) error {
	if sig.Algorithm != "ed25519" {
		return fmt.Errorf("signature algorithm %q", sig.Algorithm)
	}
	if sig.KeyID != keyID {
		return fmt.Errorf("signature key %s, catalog says %s", sig.KeyID, keyID)
	}
	raw, err := base64.StdEncoding.DecodeString(sig.Value)
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	if !ed25519.Verify(pub, checksums, raw) {
		return fmt.Errorf("signature rejected")
	}
	var got Checksums
	if err := json.Unmarshal(checksums, &got); err != nil {
		return fmt.Errorf("checksums: %w", err)
	}
	if got.Manifest != hashHex(manifest) || got.Binary != hashHex(binary) {
		return fmt.Errorf("checksums do not match package files")
	}
	return nil
}
