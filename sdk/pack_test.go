package sdk

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func testManifest() []byte {
	return []byte(`{
  "manifestVersion": 1,
  "name": "hello",
  "title": "Hello",
  "version": "0.0.1",
  "engines": {"hostApi": "1"},
  "primitives": ["tunnel.tcp"],
  "targets": [],
  "machines": ["linux", "mac"],
  "ui": false
}
`)
}

func TestPackVerify(t *testing.T) {
	key, err := GenerateKey("test-key")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := Pack(testManifest(), []byte("binary"), key)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := Open(blob)
	if err != nil {
		t.Fatal(err)
	}
	m, err := pkg.Verify(key.Public(), key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "hello" || !MachineMatch(m.Machines, "linux", "", nil) || MachineMatch(m.Machines, "windows", "", nil) {
		t.Fatalf("%+v", m)
	}
	if !MachineMatch(m.Machines, "darwin", "", []string{"docker"}) {
		t.Fatal("darwin should match mac")
	}
	if MachineMatch([]string{"linux"}, "linux", "docker", nil) {
		t.Fatal("container runner must not match linux")
	}
	if !MachineMatch([]string{"docker"}, "linux", "docker", nil) {
		t.Fatal("container runner should match docker")
	}
	pkg.Binary = []byte("tampered")
	if _, err := pkg.Verify(key.Public(), key.ID); err == nil {
		t.Fatal("tampered binary accepted")
	}
	other, _ := GenerateKey("other")
	pkg, _ = Open(blob)
	if _, err := pkg.Verify(other.Public(), key.ID); err == nil {
		t.Fatal("wrong key accepted")
	}
	sum := sha256.Sum256(blob)
	bad := bytes.Clone(blob)
	bad[len(bad)/2] ^= 0xff
	if sha256.Sum256(bad) == sum {
		t.Fatal("flipped package kept the same hash")
	}
}

func TestParseManifestRejectsUnknownMachine(t *testing.T) {
	raw := bytes.Replace(testManifest(), []byte(`"linux", "mac"`), []byte(`"solaris"`), 1)
	if _, err := ParseManifest(raw); err == nil {
		t.Fatal("unknown machine accepted")
	}
}
