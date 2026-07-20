package magpie

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyle-visner/jaybase"
	jaybaseserver "github.com/kyle-visner/jaybase/server"
)

func TestRemoteStoreAgainstMergedJaybaseServer(t *testing.T) {
	token := strings.Repeat("w", 64)
	digest := sha256.Sum256([]byte(token))
	authJSON, err := json.Marshal(map[string]any{
		"tokens": []map[string]string{{
			"id": "magpie-integration", "role": "writer", "sha256": hex.EncodeToString(digest[:]),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, authJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := jaybaseserver.LoadAuthenticator(authPath)
	if err != nil {
		t.Fatal(err)
	}

	jaybaseStore, err := jaybase.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := jaybaseStore.Close(); err != nil {
			t.Errorf("close Jaybase store: %v", err)
		}
	})
	api, err := jaybaseserver.New(jaybaseserver.Options{
		Store:  jaybaseStore,
		Auth:   auth,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	t.Cleanup(httpServer.Close)

	store, err := openRemoteStore(httpServer.URL, token, httpServer.Client(), RemoteStoreOptions{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.WriteInitialRoot(Context{Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	note, firstRoot, err := store.UpsertNote(Context{Actor: "owner"}, "", "Hosted contract", "first", "internal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSnapshot(Context{Actor: "owner"}, "integration-checkpoint"); err != nil {
		t.Fatal(err)
	}
	_, secondRoot, err := store.UpsertNote(Context{Actor: "owner"}, note.ID, note.Title, "second", note.Sensitivity)
	if err != nil {
		t.Fatal(err)
	}
	if firstRoot == secondRoot {
		t.Fatal("expected note update to advance the hosted root")
	}
	if _, err := store.CreateSnapshot(Context{Actor: "owner"}, "integration-checkpoint"); err != nil {
		t.Fatal(err)
	}

	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Root != secondRoot || state.Notes[note.ID].Body != "second" {
		t.Fatalf("merged Jaybase replay mismatch: %#v", state)
	}
	ref, err := jaybaseStore.NamedRef("integration-checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if ref != secondRoot {
		t.Fatalf("conditional named ref = %q, want %q", ref, secondRoot)
	}
	audit, err := store.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 3 || audit[0].Actor != "magpie-integration" || len(audit[0].Payload) != 0 {
		t.Fatalf("unexpected hosted audit response: %#v", audit)
	}
}
