package magpie

import (
	"fmt"
	"os"
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
