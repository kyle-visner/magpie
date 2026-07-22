package magpie

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	refPUTs       int
	expectedRoots []string
	advanceReplay bool
}

func newHostedJaybaseStub() *hostedJaybaseStub {
	return &hostedJaybaseStub{
		namedRefs:   map[string]string{},
		idempotency: map[string]string{},
	}
}

func (s *hostedJaybaseStub) appendForeign(typ string, payload any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(payload)
	hash := fmt.Sprintf("sha256:%064x", len(s.events)+1)
	parents := []string{}
	if s.root != "" {
		parents = []string{s.root}
	}
	s.events = append(s.events, jaybase.Node{
		Schema: 1, Hash: hash, Type: typ, Parents: parents, Payload: raw,
		Actor: "martin-client", Role: "writer", Command: "foreign append", CreatedAt: time.Now().UTC(),
	})
	s.root = hash
	return hash
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
		s.refPUTs++
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

type loseFirstRefPUTResponseTransport struct {
	base http.RoundTripper
	lost atomic.Bool
}

func (t *loseFirstRefPUTResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if request.Method == http.MethodPut && strings.HasPrefix(request.URL.Path, "/v1/refs/") && t.lost.CompareAndSwap(false, true) {
		_, copyErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return nil, errors.New("simulated lost named-ref response")
	}
	return response, nil
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

func TestRemoteStoreInterleavesForeignEventsAndAppendsAtSharedRoot(t *testing.T) {
	stub := newHostedJaybaseStub()
	server := httptest.NewServer(stub)
	defer server.Close()

	store, err := openRemoteStore(server.URL, "writer-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.WriteInitialRoot(Context{Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	foreignRoot := stub.appendForeign("martin.contact.updated", map[string]any{
		"schema": "not-a-magpie-envelope",
	})
	note, noteRoot, err := store.UpsertNote(Context{Actor: "owner"}, "", "Hosted shared history", "works", "internal")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Root != noteRoot || state.Notes[note.ID].Body != "works" {
		t.Fatalf("hosted shared replay produced unexpected state: %#v", state)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.expectedRoots) != 2 || stub.expectedRoots[1] != foreignRoot {
		t.Fatalf("hosted Magpie append did not use foreign head: %#v", stub.expectedRoots)
	}
	if len(stub.events) != 3 || stub.events[2].Parents[0] != foreignRoot {
		t.Fatalf("unexpected hosted event chain: %#v", stub.events)
	}
}

func TestRemoteNamedRefReconcilesLostSuccessResponse(t *testing.T) {
	stub := newHostedJaybaseStub()
	server := httptest.NewServer(stub)
	defer server.Close()

	client := server.Client()
	loss := &loseFirstRefPUTResponseTransport{base: client.Transport}
	client.Transport = loss
	store, err := openRemoteStore(server.URL, "writer-token", client)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root, err := store.WriteInitialRoot(Context{Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.CreateSnapshot(Context{Actor: "owner"}, "lost-response")
	if err != nil {
		t.Fatalf("snapshot should reconcile the durable ref after a lost response: %v", err)
	}
	if snapshot.Root != root || !loss.lost.Load() {
		t.Fatalf("unexpected reconciled snapshot: %#v, response lost=%t", snapshot, loss.lost.Load())
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.refPUTs != 1 || stub.namedRefs[snapshot.Name] != root {
		t.Fatalf("named ref PUT was retried or not persisted: puts=%d refs=%#v", stub.refPUTs, stub.namedRefs)
	}
}

func TestRemoteConcurrentSameRootSnapshotsAreIdempotent(t *testing.T) {
	stub := newHostedJaybaseStub()
	server := httptest.NewServer(stub)
	defer server.Close()

	stores := make([]*Store, 2)
	for i := range stores {
		store, err := openRemoteStore(server.URL, "writer-token", server.Client())
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
		defer store.Close()
	}
	root, err := stores[0].WriteInitialRoot(Context{Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, len(stores))
	for _, store := range stores {
		go func(store *Store) {
			<-start
			_, err := store.CreateSnapshot(Context{Actor: "owner"}, "concurrent")
			results <- err
		}(store)
	}
	close(start)
	for range stores {
		if err := <-results; err != nil {
			t.Fatalf("same-root concurrent snapshot should succeed: %v", err)
		}
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.namedRefs["concurrent"] != root {
		t.Fatalf("concurrent named ref = %q, want %q", stub.namedRefs["concurrent"], root)
	}
}

func TestRemoteInitialRootReconcilesConcurrentWinner(t *testing.T) {
	const winner = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var initialized atomic.Bool
	initPayload, err := json.Marshal(initEvent())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/root":
			root := ""
			if initialized.Load() {
				root = winner
			}
			writeStubJSON(w, http.StatusOK, map[string]string{"root": root})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/events":
			initialized.Store(true)
			writeStubJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]string{"code": "conflict", "message": "root changed"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/events" && initialized.Load():
			writeStubJSON(w, http.StatusOK, map[string]any{
				"events": []jaybase.Node{{
					Schema: 1, Hash: winner, Type: "store.init", Payload: initPayload,
					Actor: "owner", Command: "store init", CreatedAt: time.Now().UTC(),
				}},
				"root": winner, "has_more": false,
			})
		default:
			writeStubJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "not found"}})
		}
	}))
	defer server.Close()

	store, err := openRemoteStore(server.URL, "writer-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.WriteInitialRoot(Context{Actor: "owner"})
	if err != nil {
		t.Fatalf("initialization should accept the concurrent winner: %v", err)
	}
	if root != winner {
		t.Fatalf("initial root = %q, want concurrent winner %q", root, winner)
	}
}

func TestRemoteStoreNilClientKeepsRedirectGuard(t *testing.T) {
	store, err := openRemoteStore("http://localhost:1234", "writer-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	backend := store.db.(*remoteStorageBackend)
	if backend.client.CheckRedirect == nil || !errors.Is(backend.client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("default remote client must reject redirects")
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
