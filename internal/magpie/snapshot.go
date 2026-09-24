package magpie

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
	name, err = canonicalSnapshotRef(name)
	if err != nil {
		return Snapshot{}, err
	}
	if st.Root == "" {
		return Snapshot{}, appErr(ErrValidation, "cannot snapshot an empty store")
	}
	if err := s.writeNamedRef(name, st.Root); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Name: name, Root: st.Root}, nil
}
