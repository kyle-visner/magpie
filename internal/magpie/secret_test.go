package magpie

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretFromEnvPrefersVariableOverFile(t *testing.T) {
	t.Setenv("DEMO_TOKEN", "from-env")
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEMO_TOKEN_FILE", path)
	if got := SecretFromEnv("DEMO_TOKEN"); got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestSecretFromEnvReadsFileWhenVariableEmpty(t *testing.T) {
	t.Setenv("DEMO_TOKEN", "")
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  from-file  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEMO_TOKEN_FILE", path)
	if got := SecretFromEnv("DEMO_TOKEN"); got != "from-file" {
		t.Fatalf("got %q", got)
	}
}
