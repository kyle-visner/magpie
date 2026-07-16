package jaybase

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBDDJaybaseReplaysEncryptedAppendOnlyHistory(t *testing.T) {
	var store *Store
	var firstRoot string
	var secondRoot string
	secret := "agent-visible fact that must stay encrypted on disk"

	bddStep(t, "Given a clean Jaybase store with a deterministic clock", func() {
		var err error
		store, err = OpenStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store.SetClock(func() time.Time {
			return time.Date(2026, 7, 16, 9, 30, 0, 0, time.UTC)
		})
	})

	bddStep(t, "When two events are appended by the same actor", func() {
		var err error
		firstRoot, err = store.Append(Context{Actor: "agent", Role: "writer"}, AppendOptions{
			Type:     "fact",
			EntityID: "fact:customer-memory",
			Command:  "fact remember",
			Payload:  map[string]string{"body": secret},
		})
		if err != nil {
			t.Fatal(err)
		}
		secondRoot, err = store.Append(Context{Actor: "agent", Role: "writer"}, AppendOptions{
			Type:     "fact",
			EntityID: "fact:customer-memory",
			Command:  "fact annotate",
			Payload:  map[string]string{"body": "follow-up annotation"},
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	bddStep(t, "Then audit traversal replays the events in parent order", func() {
		root, err := store.CurrentRoot()
		if err != nil {
			t.Fatal(err)
		}
		if root != secondRoot {
			t.Fatalf("expected current root %s, got %s", secondRoot, root)
		}
		nodes, err := store.AuditLog()
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) != 2 {
			t.Fatalf("expected two nodes, got %#v", nodes)
		}
		if nodes[0].Hash != firstRoot || nodes[1].Hash != secondRoot {
			t.Fatalf("unexpected replay order: %#v", nodes)
		}
		if len(nodes[0].Parents) != 0 || len(nodes[1].Parents) != 1 || nodes[1].Parents[0] != firstRoot {
			t.Fatalf("unexpected parent chain: %#v", nodes)
		}

		payload, err := store.NodePayload(nodes[0])
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]string
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["body"] != secret {
			t.Fatalf("unexpected replayed payload: %#v", decoded)
		}
	})

	bddStep(t, "And plaintext payload data never appears in node files", func() {
		for _, root := range []string{firstRoot, secondRoot} {
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
	})
}

func TestBDDJaybaseNamedRefsRemainTamperEvident(t *testing.T) {
	var store *Store
	var checkpointRoot string

	bddStep(t, "Given an appended event and a named checkpoint", func() {
		var err error
		store, err = OpenStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		checkpointRoot, err = store.Append(Context{Actor: "agent"}, AppendOptions{
			Type:    "record",
			Command: "record create",
			Payload: map[string]string{"body": "original"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.WriteNamedRef("before-import", checkpointRoot); err != nil {
			t.Fatal(err)
		}
	})

	bddStep(t, "Then the named checkpoint records the selected root", func() {
		path := filepath.Join(store.Dir(), "refs", "named", "before-import")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(raw)) != checkpointRoot {
			t.Fatalf("unexpected checkpoint root in %s: %s", path, string(raw))
		}
	})

	bddStep(t, "When a node file is modified outside Jaybase", func() {
		path := store.NodePath(checkpointRoot)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.Replace(string(raw), "ciphertext", "ciphertexu", 1))
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	})

	bddStep(t, "Then replay refuses the tampered history", func() {
		_, err := store.AuditLog()
		if err == nil {
			t.Fatal("expected tampered node to fail integrity verification")
		}
		var app *AppError
		if !errors.As(err, &app) || app.Code != ErrValidation {
			t.Fatalf("expected integrity validation error, got %#v", err)
		}
	})
}

func bddStep(t *testing.T, text string, fn func()) {
	t.Helper()
	t.Logf("BDD: %s", text)
	fn()
}
