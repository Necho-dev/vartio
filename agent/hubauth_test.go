package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"

	"govmomi-wmks/internal/guest"
)

func signedAck(priv ed25519.PrivateKey, nonce, device, cred string) guest.HelloOK {
	return guest.HelloOK{
		OK:         true,
		Credential: cred,
		HubKey:     base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)),
		Sig:        base64.StdEncoding.EncodeToString(ed25519.Sign(priv, guest.HelloSigMessage(nonce, device, cred))),
	}
}

func TestAcceptHelloPinsAndRefuses(t *testing.T) {
	t.Setenv("GUEST_HUB_STATE", filepath.Join(t.TempDir(), "hub.json"))
	t.Setenv("GUEST_HUB_KEY", "")
	t.Setenv("GUEST_HUB_VERIFY", "")
	_, hub, _ := ed25519.GenerateKey(rand.Reader)
	_, rogue, _ := ed25519.GenerateKey(rand.Reader)
	const url = "ws://hub:8766/v1/probe/connect"

	// Legacy hub, nothing pinned yet: accepted, nothing stored.
	var e hubEntry
	if changed, err := acceptHello(&e, "n", "dev", guest.HelloOK{OK: true}); err != nil || changed {
		t.Fatalf("legacy hub: %v %v", changed, err)
	}

	// First signed hello pins the key and stores the credential.
	changed, err := acceptHello(&e, "n1", "dev", signedAck(hub, "n1", "dev", "secret"))
	if err != nil || !changed || e.Key == "" || e.Credential != "secret" || e.Device != "dev" {
		t.Fatalf("pin: %v %v %+v", changed, err, e)
	}
	if err := saveHubEntry(url, e); err != nil {
		t.Fatal(err)
	}
	e = loadHubEntry(url)
	if e.Credential != "secret" {
		t.Fatalf("reload %+v", e)
	}

	for name, ack := range map[string]guest.HelloOK{
		"rogue key":      signedAck(rogue, "n2", "dev", ""),
		"unsigned":       {OK: true},
		"replayed nonce": signedAck(hub, "old", "dev", ""),
		"swapped cred": func() guest.HelloOK {
			a := signedAck(hub, "n2", "dev", "")
			a.Credential = "attacker"
			return a
		}(),
	} {
		if _, err := acceptHello(&e, "n2", "dev", ack); !errors.Is(err, errHubIdentity) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	if _, err := acceptHello(&e, "n3", "dev", signedAck(hub, "n3", "dev", "")); err != nil {
		t.Fatalf("pinned hub: %v", err)
	}

	// GUEST_HUB_KEY overrides the pin, which lets an operator rotate the hub key.
	t.Setenv("GUEST_HUB_KEY", base64.StdEncoding.EncodeToString(rogue.Public().(ed25519.PublicKey)))
	if _, err := acceptHello(&e, "n4", "dev", signedAck(rogue, "n4", "dev", "")); err != nil {
		t.Fatalf("env key: %v", err)
	}
	if _, err := acceptHello(&e, "n5", "dev", signedAck(hub, "n5", "dev", "")); !errors.Is(err, errHubIdentity) {
		t.Fatalf("pinned key should lose to GUEST_HUB_KEY: %v", err)
	}
}
