package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRailCollectUsageMentionsMethods(t *testing.T) {
	var out bytes.Buffer
	if err := run(nil, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("collect")) {
		t.Fatalf("usage: %s", out.String())
	}
}

func TestRailBinaryStaysOutOfMagpieImportGraph(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// cmd/rail is this package; walk up to module root.
	mod := filepath.Clean(filepath.Join(root, "../.."))
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}} {{.Imports}}", "./internal/magpie")
	cmd.Dir = mod
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	if bytes.Contains(out, []byte("magpie/internal/rail")) || bytes.Contains(out, []byte("stripe-go")) {
		t.Fatalf("Magpie imported rail or Stripe: %s", out)
	}
}
