package magpie

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kyle-visner/jaybase"
)

type hostedJaybaseStub struct {
	mu            sync.Mutex
	root          string
	events        []jaybase.Node
	namedRefs     map[string]string
	idempotency   map[string]string
	authFailures  int
	expectedRoots []string
	advanceReplay bool
}

func newHostedJaybaseStub() *hostedJaybaseStub {
	return &hostedJaybaseStub{
		namedRefs:   map[string]string{},
		idempotency: map[string]string{},
	}
}

func (s *hostedJaybaseStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer writer-token" {
		s.mu.Lock()
		s.authFailures++
		s.mu.Unlock()
		writeStubJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "permission_denied", "message": "bad token"}})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/root":
		s.mu.Lock()
		root := s.root
		s.mu.Unlock()
		writeStubJSON(w, http.StatusOK, map[string]string{"root": root})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/events":
		s.eventsResponse(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/events":
		s.appendResponse(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/refs/"):
		name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/v1/refs/"))
		s.mu.Lock()
		root, ok := s.namedRefs[name]
		s.mu.Unlock()
		if !ok {
			writeStubJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "ref not found"}})
			return
		}
		writeStubJSON(w, http.StatusOK, map[string]string{"name": name, "root": root})
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/refs/"):
		var request struct {
			Root         string  `json:"root"`
			ExpectedRoot *string `json:"expected_root"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeStubJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "validation_error", "message": err.Error()}})
			return
		}
		name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/v1/refs/"))
		s.mu.Lock()
		current := s.namedRefs[name]
		if request.ExpectedRoot == nil || *request.ExpectedRoot != current {
			s.mu.Unlock()
			writeStubJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": "conflict", "message": "ref changed"}})
			return
		}
		s.namedRefs[name] = request.Root
		s.mu.Unlock()
		writeStubJSON(w, http.StatusOK, map[string]string{"name": name, "root": request.Root})
	default:
		writeStubJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "not found"}})
	}
}

func (s *hostedJaybaseStub) appendResponse(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" || len(r.Header.Get("Idempotency-Key")) < 8 {
		writeStubJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "validation_error", "message": "missing hosted write headers"}})
		return
	}
	var request struct {
		Type         string          `json:"type"`
		EntityID     string          `json:"entity_id"`
		Command      string          `json:"command"`
		Payload      json.RawMessage `json:"payload"`
		ExpectedRoot string          `json:"expected_root"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeStubJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "validation_error", "message": err.Error()}})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.expectedRoots = append(s.expectedRoots, request.ExpectedRoot)
	if request.ExpectedRoot != s.root {
		writeStubJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": "conflict", "message": "root changed"}})
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if hash, ok := s.idempotency[key]; ok {
		writeStubJSON(w, http.StatusOK, map[string]any{"hash": hash, "root": s.root, "replayed": true})
		return
	}
	hash := fmt.Sprintf("sha256:%064x", len(s.events)+1)
	parents := []string{}
	if s.root != "" {
		parents = []string{s.root}
	}
	s.events = append(s.events, jaybase.Node{
		Schema: 1, Hash: hash, Type: request.Type, EntityID: request.EntityID,
		Parents: parents, Payload: request.Payload, Actor: "magpie-client", Role: "writer",
		Command: request.Command, CreatedAt: time.Now().UTC(),
	})
	s.root = hash
	s.idempotency[key] = hash
	writeStubJSON(w, http.StatusCreated, map[string]any{"hash": hash, "root": hash, "replayed": false})
}

func (s *hostedJaybaseStub) eventsResponse(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	if after := r.URL.Query().Get("after"); after != "" {
		for i, event := range s.events {
			if event.Hash == after {
				start = i + 1
				break
			}
		}
	}
	end := start + 1 // Force pagination so the client cursor path is exercised.
	if end > len(s.events) {
		end = len(s.events)
	}
	events := append([]jaybase.Node(nil), s.events[start:end]...)
	if r.URL.Query().Get("include_payload") != "true" {
		for i := range events {
			events[i].Payload = nil
		}
	}
	writeStubJSON(w, http.StatusOK, map[string]any{
		"events": events, "root": s.root, "has_more": end < len(s.events),
	})
	if s.advanceReplay {
		s.root = fmt.Sprintf("sha256:%064x", len(s.events)+1000)
		s.advanceReplay = false
	}
}

func writeStubJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func TestRemoteStoreUsesHostedJaybaseContract(t *testing.T) {
	stub := newHostedJaybaseStub()
	server := httptest.NewServer(stub)
	defer server.Close()

	store, err := openRemoteStore(server.URL, "writer-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initRoot, err := store.WriteInitialRoot(Context{Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	note, noteRoot, err := store.UpsertNote(Context{Actor: "owner"}, "", "Hosted note", "remote payload", "internal")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Root != noteRoot || len(state.Notes) != 1 {
		t.Fatalf("remote replay did not reconstruct state: %#v", state)
	}
	audit, err := store.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 2 || len(audit[1].Payload) != 0 {
		t.Fatalf("expected two paginated metadata-only audit events, got %#v", audit)
	}
	if _, err := store.CreateSnapshot(Context{Actor: "owner"}, "before-close"); err != nil {
		t.Fatal(err)
	}
	_, updatedRoot, err := store.UpsertNote(Context{Actor: "owner"}, note.ID, "Hosted note", "updated remote payload", "internal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSnapshot(Context{Actor: "owner"}, "before-close"); err != nil {
		t.Fatal(err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.authFailures != 0 {
		t.Fatalf("hosted requests omitted bearer authentication %d times", stub.authFailures)
	}
	if len(stub.idempotency) != 3 {
		t.Fatalf("expected a stable idempotency key per append, got %d", len(stub.idempotency))
	}
	if len(stub.expectedRoots) != 3 || stub.expectedRoots[0] != "" || stub.expectedRoots[1] != initRoot || stub.expectedRoots[2] != noteRoot {
		t.Fatalf("unexpected optimistic concurrency roots: %#v", stub.expectedRoots)
	}
	if stub.namedRefs["before-close"] != updatedRoot {
		t.Fatalf("named ref was not written through the hosted API: %#v", stub.namedRefs)
	}
}

func TestRemoteStoreMapsJaybaseErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/root" {
			writeStubJSON(w, http.StatusOK, map[string]string{"root": ""})
			return
		}
		writeStubJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{"code": "conflict", "message": "root changed"},
		})
	}))
	defer server.Close()

	store, err := openRemoteStore(server.URL, "writer-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.WriteInitialRoot(Context{Actor: "owner"})
	appError, ok := err.(*AppError)
	if !ok || appError.Code != ErrConflict {
		t.Fatalf("expected conflict AppError, got %T %v", err, err)
	}
}

func TestRemoteStoreRejectsWriteWhenValidatedRootChanged(t *testing.T) {
	stub := newHostedJaybaseStub()
	server := httptest.NewServer(stub)
	defer server.Close()

	store, err := openRemoteStore(server.URL, "writer-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	initRoot, err := store.WriteInitialRoot(Context{Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stub.advanceReplay = true
	stub.mu.Unlock()

	_, _, err = store.UpsertNote(Context{Actor: "owner"}, "", "Concurrent note", "body", "internal")
	appError, ok := err.(*AppError)
	if !ok || appError.Code != ErrConflict {
		t.Fatalf("expected a root conflict instead of appending stale business state, got %T %v", err, err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if got := stub.expectedRoots[len(stub.expectedRoots)-1]; got != initRoot {
		t.Fatalf("write used %q instead of validated root %q", got, initRoot)
	}
}

func TestRemoteStoreRequiresSafeConnectionInputs(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		token string
	}{
		{name: "missing token", url: "https://jaybase.example.com"},
		{name: "plain HTTP", url: "http://jaybase.example.com", token: "token"},
		{name: "URL credentials", url: "https://user@jaybase.example.com", token: "token"},
		{name: "URL path", url: "https://jaybase.example.com/api", token: "token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := OpenRemoteStore(test.url, test.token); err == nil {
				t.Fatal("expected unsafe hosted connection input to be rejected")
			}
		})
	}
}

func TestRemoteStoreDoesNotRetryIntegrityErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeStubJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"code": "integrity_error", "message": "stored data failed verification"},
		})
	}))
	defer server.Close()

	store, err := openRemoteStore(server.URL, "writer-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.currentRoot()
	appError, ok := err.(*AppError)
	if !ok || appError.Code != ErrIntegrity {
		t.Fatalf("expected integrity error, got %T %v", err, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("integrity failure must not be retried, got %d requests", calls.Load())
	}
}

func TestRemoteStoreRetriesInternalErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			writeStubJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"code": "internal_error", "message": "temporary failure"},
			})
			return
		}
		writeStubJSON(w, http.StatusOK, map[string]string{"root": ""})
	}))
	defer server.Close()

	store, err := openRemoteStore(server.URL, "writer-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.currentRoot(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected two bounded retries, got %d requests", calls.Load())
	}
}
