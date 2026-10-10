package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	sum := strings.Repeat("ab", 32)
	a := Artifact{OS: "windows", Arch: "amd64", Size: 10, SHA256: sum}
	a.Sig = Sign(priv, "1.9.0", a)
	keys := []ed25519.PublicKey{other, pub}

	if err := Verify(keys, "1.9.0", a, 10, strings.ToUpper(sum)); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	if err := Verify(keys, "1.9.0", a, 11, sum); err == nil {
		t.Fatal("size mismatch accepted")
	}
	if err := Verify(keys, "1.9.1", a, 10, sum); err == nil {
		t.Fatal("signature replayed for another version")
	}
	b := a
	b.OS = "linux"
	if err := Verify(keys, "1.9.0", b, 10, sum); err == nil {
		t.Fatal("signature replayed for another platform")
	}
	if err := Verify([]ed25519.PublicKey{other}, "1.9.0", a, 10, sum); err == nil {
		t.Fatal("untrusted key accepted")
	}
	b = a
	b.Sig = ""
	if err := Verify(keys, "1.9.0", b, 10, sum); err != ErrUnsigned {
		t.Fatalf("unsigned: got %v", err)
	}
}

func TestParseKeys(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	keys, err := ParseKeys("# comment\n\n" + base64.StdEncoding.EncodeToString(pub) + "\n")
	if err != nil || len(keys) != 1 || !keys[0].Equal(pub) {
		t.Fatalf("ParseKeys = %v, %v", keys, err)
	}
	if _, err := ParseKeys("nope"); err == nil {
		t.Fatal("garbage key accepted")
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.10.0", "1.9.2", 1}, {"1.8.1", "1.8.1", 0}, {"1.8", "1.8.0", 0}, {"1.8.0", "1.8.1", -1}} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
