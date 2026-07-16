package jaybase

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
	"strings"
	"time"
)

const schemaVersion = 1

type ErrorCode string

const (
	ErrValidation ErrorCode = "validation_error"
	ErrPermission ErrorCode = "permission_denied"
	ErrNotFound   ErrorCode = "not_found"
	ErrConflict   ErrorCode = "conflict"
)

type AppError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *AppError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func appErr(code ErrorCode, format string, args ...any) *AppError {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...)}
}

type Store struct {
	dir string
	now func() time.Time
	key []byte
}

type Context struct {
	Actor string
	Role  string
}

type AppendOptions struct {
	Type      string
	EntityID  string
	Command   string
	Payload   any
	CreatedAt time.Time
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

func Open(dir string) (*Store, error) {
	return OpenStore(dir)
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

func (s *Store) SetClock(now func() time.Time) {
	if now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
		return
	}
	s.now = now
}

func (s *Store) Dir() string {
	return s.dir
}

func (s *Store) rootPath() string {
	return filepath.Join(s.dir, "refs", "root")
}

func (s *Store) CurrentRoot() (string, error) {
	b, err := os.ReadFile(s.rootPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (s *Store) Append(ctx Context, opts AppendOptions) (string, error) {
	opts.Type = strings.TrimSpace(opts.Type)
	opts.EntityID = strings.TrimSpace(opts.EntityID)
	opts.Command = strings.TrimSpace(opts.Command)
	if opts.Type == "" {
		return "", appErr(ErrValidation, "node type is required")
	}
	root, err := s.CurrentRoot()
	if err != nil {
		return "", err
	}
	parents := []string{}
	if root != "" {
		parents = []string{root}
	}
	raw, err := json.Marshal(opts.Payload)
	if err != nil {
		return "", err
	}
	sealed, err := encryptPayload(s.key, raw)
	if err != nil {
		return "", err
	}
	created := opts.CreatedAt
	if created.IsZero() {
		created = s.now()
	}
	created = created.UTC().Truncate(time.Microsecond)
	content := nodeContent{
		Schema: schemaVersion, Type: opts.Type, EntityID: opts.EntityID, Parents: parents,
		SealedPayload: sealed, Actor: ctx.Actor, Role: ctx.Role, Command: opts.Command, CreatedAt: created,
	}
	contentBytes, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(contentBytes)
	hash := "sha256:" + hex.EncodeToString(sum[:])
	node := Node{
		Schema: schemaVersion, Hash: hash, Type: opts.Type, EntityID: opts.EntityID, Parents: parents,
		SealedPayload: sealed, Actor: ctx.Actor, Role: ctx.Role, Command: opts.Command, CreatedAt: created,
	}
	path := s.NodePath(hash)
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

func (s *Store) NodePath(hash string) string {
	name := strings.TrimPrefix(hash, "sha256:")
	return filepath.Join(s.dir, "objects", "nodes", name+".json")
}

func (s *Store) readNode(hash string) (Node, error) {
	var node Node
	b, err := os.ReadFile(s.NodePath(hash))
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
	root, err := s.CurrentRoot()
	if err != nil {
		return nil, err
	}
	return s.NodesFromRoot(root)
}

func (s *Store) NodePayload(node Node) ([]byte, error) {
	if node.SealedPayload != nil {
		return decryptPayload(s.key, node.SealedPayload)
	}
	if len(node.Payload) > 0 {
		return node.Payload, nil
	}
	return nil, appErr(ErrValidation, "node %s has no payload", node.Hash)
}

func (s *Store) WriteNamedRef(name string, root string) error {
	name = strings.TrimSpace(name)
	root = strings.TrimSpace(root)
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return appErr(ErrValidation, "named ref must be a simple file-safe name")
	}
	if root == "" {
		return appErr(ErrValidation, "named ref root is required")
	}
	path := filepath.Join(s.dir, "refs", "named", name)
	return os.WriteFile(path, []byte(root+"\n"), 0o600)
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
