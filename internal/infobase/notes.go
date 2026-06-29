package infobase

import (
	"strings"
)

type noteUpsertPayload struct {
	Note Note `json:"note"`
}

func (s *Store) UpsertNote(ctx Context, id, title, body, sensitivity string) (Note, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Note{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionNotesWrite); err != nil {
		return Note{}, "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return Note{}, "", appErr(ErrValidation, "note title is required")
	}
	if id == "" {
		id = makeID("note", strings.ToLower(title))
	}
	if sensitivity == "" {
		sensitivity = "internal"
	}
	now := s.now().UTC()
	note := Note{ID: id, Title: title, Body: body, Sensitivity: sensitivity, UpdatedAt: now, UpdatedBy: ctx.Actor}
	if existing, ok := st.Notes[id]; ok {
		note.CreatedAt = existing.CreatedAt
		note.CreatedBy = existing.CreatedBy
	} else {
		note.CreatedAt = now
		note.CreatedBy = ctx.Actor
	}
	hash, err := s.appendEvent(ctx, "note", id, "note upsert", wrapEvent("note.upsert", noteUpsertPayload{Note: note}), true)
	return note, hash, err
}

func (s *Store) GetNote(ctx Context, id string) (Note, error) {
	st, err := s.LoadState()
	if err != nil {
		return Note{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionNotesRead); err != nil {
		return Note{}, err
	}
	note, ok := st.Notes[id]
	if !ok {
		return Note{}, appErr(ErrNotFound, "note %s not found", id)
	}
	return note, nil
}
