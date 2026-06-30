package infobase

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const schemaVersion = 1

type Store struct {
	dir string
	now func() time.Time
	key []byte
}

type Node struct {
	Schema        int               `json:"schema"`
	Hash          string            `json:"hash"`
	Type          string            `json:"type"`
	EntityID      string            `json:"entity_id,omitempty"`
	Parents       []string          `json:"parents"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
	SealedPayload *EncryptedPayload `json:"sealed_payload,omitempty"`
	Actor         string            `json:"actor"`
	Role          string            `json:"role"`
	Command       string            `json:"command"`
	CreatedAt     time.Time         `json:"created_at"`
}

type nodeContent struct {
	Schema        int               `json:"schema"`
	Type          string            `json:"type"`
	EntityID      string            `json:"entity_id,omitempty"`
	Parents       []string          `json:"parents"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
	SealedPayload *EncryptedPayload `json:"sealed_payload,omitempty"`
	Actor         string            `json:"actor"`
	Role          string            `json:"role"`
	Command       string            `json:"command"`
	CreatedAt     time.Time         `json:"created_at"`
}

type EncryptedPayload struct {
	Algorithm  string `json:"algorithm"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type eventEnvelope struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		dir = ".infobase"
	}
	s := &Store{dir: dir, now: func() time.Time { return time.Now().UTC() }}
	for _, child := range []string{"objects/nodes", "refs/named", "keys"} {
		if err := os.MkdirAll(filepath.Join(dir, child), 0o700); err != nil {
			return nil, err
		}
	}
	key, err := loadOrCreateKey(dir)
	if err != nil {
		return nil, err
	}
	s.key = key
	return s, nil
}

func (s *Store) Dir() string {
	return s.dir
}

func (s *Store) rootPath() string {
	return filepath.Join(s.dir, "refs", "root")
}

func (s *Store) currentRoot() (string, error) {
	b, err := os.ReadFile(s.rootPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (s *Store) WriteInitialRoot(ctx Context) (string, error) {
	root, err := s.currentRoot()
	if err != nil {
		return "", err
	}
	if root != "" {
		return root, nil
	}
	event := initEvent()
	return s.appendEvent(ctx, "store.init", "", "store init", event, true)
}

func (s *Store) appendEvent(ctx Context, typ, entityID, command string, payload any, skipRootCheck bool) (string, error) {
	if !skipRootCheck {
		if _, err := s.LoadState(); err != nil {
			return "", err
		}
	}
	root, err := s.currentRoot()
	if err != nil {
		return "", err
	}
	parents := []string{}
	if root != "" {
		parents = []string{root}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sealed, err := encryptPayload(s.key, raw)
	if err != nil {
		return "", err
	}
	created := s.now().UTC().Truncate(time.Microsecond)
	content := nodeContent{
		Schema: schemaVersion, Type: typ, EntityID: entityID, Parents: parents,
		SealedPayload: sealed, Actor: ctx.Actor, Role: ctx.Role, Command: command, CreatedAt: created,
	}
	contentBytes, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(contentBytes)
	hash := "sha256:" + hex.EncodeToString(sum[:])
	node := Node{
		Schema: schemaVersion, Hash: hash, Type: typ, EntityID: entityID, Parents: parents,
		SealedPayload: sealed, Actor: ctx.Actor, Role: ctx.Role, Command: command, CreatedAt: created,
	}
	path := s.nodePath(hash)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		nodeBytes, err := json.MarshalIndent(node, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, append(nodeBytes, '\n'), 0o600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := os.WriteFile(s.rootPath(), []byte(hash+"\n"), 0o600); err != nil {
		return "", err
	}
	return hash, nil
}

func (s *Store) nodePath(hash string) string {
	name := strings.TrimPrefix(hash, "sha256:")
	return filepath.Join(s.dir, "objects", "nodes", name+".json")
}

func (s *Store) readNode(hash string) (Node, error) {
	var node Node
	b, err := os.ReadFile(s.nodePath(hash))
	if err != nil {
		return node, err
	}
	if err := json.Unmarshal(b, &node); err != nil {
		return node, err
	}
	if err := verifyNode(node); err != nil {
		return node, err
	}
	return node, nil
}

func verifyNode(node Node) error {
	content := nodeContent{
		Schema: node.Schema, Type: node.Type, EntityID: node.EntityID, Parents: node.Parents,
		Payload: node.Payload, SealedPayload: node.SealedPayload, Actor: node.Actor, Role: node.Role, Command: node.Command, CreatedAt: node.CreatedAt,
	}
	contentBytes, err := json.Marshal(content)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(contentBytes)
	expected := "sha256:" + hex.EncodeToString(sum[:])
	if expected != node.Hash {
		return appErr(ErrValidation, "node integrity check failed for %s", node.Hash)
	}
	return nil
}

func (s *Store) NodesFromRoot(root string) ([]Node, error) {
	if root == "" {
		return nil, nil
	}
	var reversed []Node
	seen := map[string]bool{}
	for root != "" {
		if seen[root] {
			return nil, appErr(ErrValidation, "cycle detected while walking DAG at %s", root)
		}
		seen[root] = true
		node, err := s.readNode(root)
		if err != nil {
			return nil, err
		}
		reversed = append(reversed, node)
		if len(node.Parents) == 0 {
			break
		}
		if len(node.Parents) > 1 {
			return nil, appErr(ErrValidation, "merge roots are not supported in phase 1")
		}
		root = node.Parents[0]
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed, nil
}

func (s *Store) AuditLog() ([]Node, error) {
	root, err := s.currentRoot()
	if err != nil {
		return nil, err
	}
	return s.NodesFromRoot(root)
}

func (s *Store) LoadState() (State, error) {
	root, err := s.currentRoot()
	if err != nil {
		return State{}, err
	}
	st := emptyState()
	nodes, err := s.NodesFromRoot(root)
	if err != nil {
		return State{}, err
	}
	for _, node := range nodes {
		if err := s.applyNode(&st, node); err != nil {
			return State{}, err
		}
		st.Root = node.Hash
	}
	return st, nil
}

func (s *Store) applyNode(st *State, node Node) error {
	var env eventEnvelope
	payload, err := s.nodePayload(node)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return err
	}
	switch env.Kind {
	case "init":
		var ev initPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Roles = ev.Roles
		st.Users = ev.Users
		st.Settings = ev.Settings
		if st.Settings.AccountingBasis == "" {
			st.Settings = DefaultBookSettings()
		}
	case "role.upsert":
		var ev roleUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Roles[ev.Role.Name] = ev.Role
	case "user.upsert":
		var ev userUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Users[ev.User.ID] = ev.User
	case "account.create":
		var ev accountCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Accounts[ev.Account.ID] = ev.Account
	case "account.update":
		var ev accountUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if _, ok := st.Accounts[ev.Account.ID]; !ok {
			return appErr(ErrValidation, "account.update references unknown account %s", ev.Account.ID)
		}
		st.Accounts[ev.Account.ID] = ev.Account
	case "journal.create":
		var ev journalCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if ev.Entry.AccountingBasis == "" {
			ev.Entry.AccountingBasis = st.effectiveSettings().AccountingBasis
		}
		st.JournalEntries[ev.Entry.ID] = ev.Entry
		if ev.SourceKey != "" {
			st.SourceKeys[ev.SourceKey] = ev.Entry.ID
		}
	case "settings.update":
		var ev settingsUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if _, err := normalizeAccountingBasis(ev.Settings.AccountingBasis); err != nil {
			return err
		}
		st.Settings = ev.Settings
		if st.Settings.ModifiedCashPolicy == (ModifiedCashPolicy{}) {
			st.Settings.ModifiedCashPolicy = DefaultModifiedCashPolicy()
		}
	case "note.upsert":
		var ev noteUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Notes[ev.Note.ID] = ev.Note
	default:
		return appErr(ErrValidation, "unknown event kind %q in node %s", env.Kind, node.Hash)
	}
	return nil
}

func (s *Store) nodePayload(node Node) ([]byte, error) {
	if node.SealedPayload != nil {
		return decryptPayload(s.key, node.SealedPayload)
	}
	if len(node.Payload) > 0 {
		return node.Payload, nil
	}
	return nil, appErr(ErrValidation, "node %s has no payload", node.Hash)
}

func loadOrCreateKey(dir string) ([]byte, error) {
	if raw := os.Getenv("INFOBASE_DATA_KEY"); raw != "" {
		key, err := decodeKey(raw)
		if err != nil {
			return nil, err
		}
		return key, nil
	}
	path := filepath.Join(dir, "keys", "data.key")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(key)
		if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeKey(strings.TrimSpace(string(b)))
}

func decodeKey(raw string) ([]byte, error) {
	if key, err := base64.StdEncoding.DecodeString(raw); err == nil && len(key) == 32 {
		return key, nil
	}
	if key, err := hex.DecodeString(raw); err == nil && len(key) == 32 {
		return key, nil
	}
	return nil, appErr(ErrValidation, "INFOBASE_DATA_KEY or store key must be 32 bytes encoded as base64 or hex")
}

func encryptPayload(key []byte, plaintext []byte) (*EncryptedPayload, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return &EncryptedPayload{
		Algorithm:  "AES-256-GCM",
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

func decryptPayload(key []byte, sealed *EncryptedPayload) ([]byte, error) {
	if sealed.Algorithm != "AES-256-GCM" {
		return nil, appErr(ErrValidation, "unsupported payload encryption algorithm %q", sealed.Algorithm)
	}
	nonce, err := base64.StdEncoding.DecodeString(sealed.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(sealed.Ciphertext)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, appErr(ErrValidation, "encrypted payload authentication failed")
	}
	return plaintext, nil
}

func wrapEvent(kind string, data any) eventEnvelope {
	raw, _ := json.Marshal(data)
	return eventEnvelope{Kind: kind, Data: raw}
}

func makeID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%s:%s", prefix, hex.EncodeToString(h.Sum(nil))[:24])
}

func sortedPermissions(perms []Permission) []Permission {
	out := append([]Permission(nil), perms...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
