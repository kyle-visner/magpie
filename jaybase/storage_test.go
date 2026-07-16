package jaybase

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAppendAuditPayloadAndEncryption(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.SetClock(func() time.Time {
		return time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	})

	secret := "Cardholder data must not appear in plaintext."
	root, err := store.Append(Context{Actor: "agent", Role: "writer"}, AppendOptions{
		Type:     "note",
		EntityID: "note:1",
		Command:  "note put",
		Payload:  map[string]string{"secret": secret},
	})
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := store.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Hash != root {
		t.Fatalf("unexpected audit nodes: %#v", nodes)
	}

	payload, err := store.NodePayload(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["secret"] != secret {
		t.Fatalf("unexpected payload: %#v", decoded)
	}

	onDisk, err := os.ReadFile(store.NodePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), secret) {
		t.Fatalf("node file contains plaintext sensitive content: %s", store.NodePath(root))
	}
	if !strings.Contains(string(onDisk), "AES-256-GCM") {
		t.Fatalf("node file does not advertise payload encryption: %s", string(onDisk))
	}
}

func TestTamperingIsDetected(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.Append(Context{Actor: "agent"}, AppendOptions{
		Type:    "record",
		Command: "record create",
		Payload: map[string]string{"body": "original"},
	})
	if err != nil {
		t.Fatal(err)
	}

	path := store.NodePath(root)
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	onDisk = []byte(strings.Replace(string(onDisk), "ciphertext", "ciphertexu", 1))
	if err := os.WriteFile(path, onDisk, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = store.AuditLog()
	if err == nil {
		t.Fatal("expected tampered node to fail integrity verification")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected integrity validation error, got %#v", err)
	}
}
