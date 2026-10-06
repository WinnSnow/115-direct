package secure

import (
	"path/filepath"
	"testing"
)

func TestVaultRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	vault, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal("UID=secret")
	if err != nil {
		t.Fatal(err)
	}
	if sealed == "UID=secret" {
		t.Fatal("value was not encrypted")
	}
	plain, err := vault.Open(sealed)
	if err != nil || plain != "UID=secret" {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err = second.Open(sealed)
	if err != nil || plain != "UID=secret" {
		t.Fatal("persisted key cannot decrypt")
	}
}
