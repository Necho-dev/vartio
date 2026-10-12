package systems

import (
	"bytes"
	"testing"
)

func TestPostgresJSONDropsNullByte(t *testing.T) {
	in := []byte(`{"product":"\u0000acBookPro18,3","keep":"\\u0000"}`)
	got := postgresJSON(in)
	want := []byte(`{"product":"acBookPro18,3","keep":"\\u0000"}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("got %s", got)
	}
}

func TestPostgresJSONLeavesCleanInput(t *testing.T) {
	in := []byte(`{"product":"MacBookPro18,3"}`)
	if !bytes.Equal(postgresJSON(in), in) {
		t.Fatal("clean JSON was copied")
	}
	if postgresJSON(nil) != nil {
		t.Fatal("nil should stay nil")
	}
}
