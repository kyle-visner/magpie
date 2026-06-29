package infobase

import (
	"os"
	"path/filepath"
	"strings"
)

type Snapshot struct {
	Name string `json:"name"`
	Root string `json:"root"`
}

func (s *Store) CreateSnapshot(ctx Context, name string) (Snapshot, error) {
	st, err := s.LoadState()
	if err != nil {
		return Snapshot{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionSnapshot); err != nil {
		return Snapshot{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return Snapshot{}, appErr(ErrValidation, "snapshot name must be a simple file-safe name")
	}
	if st.Root == "" {
		return Snapshot{}, appErr(ErrValidation, "cannot snapshot an empty store")
	}
	path := filepath.Join(s.dir, "refs", "named", name)
	if err := os.WriteFile(path, []byte(st.Root+"\n"), 0o600); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Name: name, Root: st.Root}, nil
}
