package magpie

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHostedStateCacheInvalidatesCorruptCheckpoint(t *testing.T) {
	cache, err := newHostedStateCache(t.TempDir(), "https://jaybase.example.com", "writer-token")
	if err != nil {
		t.Fatal(err)
	}
	state := emptyState()
	state.Root = "sha256:cached"
	state.Notes["note:secret"] = Note{ID: "note:secret", Body: "sensitive body"}
	if err := cache.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.path, []byte(`{"version":1,"nonce":"broken"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.Load(); err != nil || found {
		t.Fatalf("corrupt checkpoint load = found %t err %v", found, err)
	}
	if _, err := os.Stat(cache.path); !os.IsNotExist(err) {
		t.Fatalf("corrupt checkpoint was not removed: %v", err)
	}
}

func TestHostedStateCacheInvalidatesOldMaterialization(t *testing.T) {
	cache, err := newHostedStateCache(t.TempDir(), "https://jaybase.example.com", "writer-token")
	if err != nil {
		t.Fatal(err)
	}
	state := emptyState()
	state.Root = "sha256:old-materialization"
	plaintext, err := json.Marshal(stateCheckpoint{
		MaterializationVersion: hostedStateMaterializationVersion + 1,
		State:                  state,
	})
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cache.aead()
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	raw, err := json.Marshal(encryptedStateCheckpoint{
		Version:    hostedStateCacheEnvelopeVersion,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, cache.aad),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.Load(); err != nil || found {
		t.Fatalf("old materialization load = found %t err %v", found, err)
	}
	if _, err := os.Stat(cache.path); !os.IsNotExist(err) {
		t.Fatalf("old materialization checkpoint was not removed: %v", err)
	}
}

func TestHostedStateCacheSeparatesCredentials(t *testing.T) {
	dir := t.TempDir()
	first, err := newHostedStateCache(dir, "https://jaybase.example.com", "first-token")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newHostedStateCache(dir, "https://jaybase.example.com", "second-token")
	if err != nil {
		t.Fatal(err)
	}
	if first.path == second.path || first.key == second.key {
		t.Fatal("hosted caches must be separated by credential")
	}
}

func TestHostedStateCacheConcurrentSavesRemainAtomic(t *testing.T) {
	cache, err := newHostedStateCache(t.TempDir(), "https://jaybase.example.com", "writer-token")
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for i := 0; i < writers; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			state := emptyState()
			state.Root = fmt.Sprintf("sha256:%064x", i+1)
			errorsByWriter <- cache.Save(state)
		}(i)
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, found, err := cache.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !found || state.Root == "" {
		t.Fatalf("concurrent saves left no readable checkpoint: found=%t state=%#v", found, state)
	}
}

func TestHostedStateCacheCreatesPrivateSubdirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cache, err := newHostedStateCache(dir, "https://jaybase.example.com", "writer-token")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cache.dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("private cache directory mode = %o, want 700", info.Mode().Perm())
	}
}

func TestHostedStateCacheRejectsWritableSharedBaseDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := newHostedStateCache(dir, "https://jaybase.example.com", "writer-token"); err == nil {
		t.Fatal("expected writable shared cache base directory to be rejected")
	}
}

func TestHostedStateCacheRejectsStickySharedBaseDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if _, err := newHostedStateCache(dir, "https://jaybase.example.com", "writer-token"); err == nil {
		t.Fatal("expected sticky shared cache base directory to be rejected")
	}
}

func TestHostedStateCacheRejectsSymlinkedDirectories(t *testing.T) {
	t.Run("base", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "cache-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := newHostedStateCache(link, "https://jaybase.example.com", "writer-token"); err == nil {
			t.Fatal("expected symlinked cache base to be rejected")
		}
	})

	t.Run("state", func(t *testing.T) {
		base := t.TempDir()
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(base, "state")); err != nil {
			t.Fatal(err)
		}
		if _, err := newHostedStateCache(base, "https://jaybase.example.com", "writer-token"); err == nil {
			t.Fatal("expected symlinked cache state directory to be rejected")
		}
	})
}
