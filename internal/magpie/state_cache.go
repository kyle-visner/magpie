package magpie

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	hostedStateCacheEnvelopeVersion = 1
	// hostedStateMaterializationVersion must be bumped whenever State or
	// applyNode changes how an event history projects into materialized state.
	// A mismatch invalidates the checkpoint and forces a cold replay.
	hostedStateMaterializationVersion = 1
)

type hostedStateCache struct {
	dir  string
	path string
	key  [sha256.Size]byte
	aad  []byte
}

type encryptedStateCheckpoint struct {
	Version    int    `json:"version"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type stateCheckpoint struct {
	MaterializationVersion int   `json:"materialization_version"`
	State                  State `json:"state"`
}

func newHostedStateCache(dir, baseURL, token string) (*hostedStateCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create Magpie cache base directory: %w", err)
	}
	baseInfo, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat Magpie cache base directory: %w", err)
	}
	if !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 {
		return nil, appErr(ErrValidation, "MAGPIE_CACHE_DIR must be a real directory, not a symlink")
	}
	if baseInfo.Mode().Perm()&0o022 != 0 {
		return nil, appErr(ErrPermission, "MAGPIE_CACHE_DIR must not be writable by group or other users")
	}
	privateDir := filepath.Join(dir, "state")
	if err := os.Mkdir(privateDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create Magpie cache directory: %w", err)
	}
	info, err := os.Lstat(privateDir)
	if err != nil {
		return nil, fmt.Errorf("stat Magpie cache directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, appErr(ErrValidation, "Magpie cache state path must be a real directory, not a symlink")
	}
	if err := os.Chmod(privateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure Magpie cache directory: %w", err)
	}
	info, err = os.Lstat(privateDir)
	if err != nil {
		return nil, fmt.Errorf("restat Magpie cache directory: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, appErr(ErrPermission, "MAGPIE_CACHE_DIR must not be accessible by group or other users")
	}

	key := cacheMAC(token, "magpie-hosted-state-key-v1\x00"+baseURL)
	identity := cacheMAC(token, "magpie-hosted-state-id-v1\x00"+baseURL)
	name := "hosted-state-v1-" + hex.EncodeToString(identity[:16]) + ".json"
	return &hostedStateCache{
		dir:  privateDir,
		path: filepath.Join(privateDir, name),
		key:  key,
		aad:  []byte(fmt.Sprintf("magpie-hosted-state-cache:%d:%s", hostedStateCacheEnvelopeVersion, baseURL)),
	}, nil
}

func cacheMAC(token, message string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(message))
	var result [sha256.Size]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func (c *hostedStateCache) Load() (State, bool, error) {
	info, err := os.Lstat(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("stat Magpie state checkpoint: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return State{}, false, appErr(ErrPermission, "Magpie state checkpoint must be a private regular file")
	}

	raw, err := os.ReadFile(c.path)
	if err != nil {
		return State{}, false, fmt.Errorf("read Magpie state checkpoint: %w", err)
	}
	var encrypted encryptedStateCheckpoint
	if err := json.Unmarshal(raw, &encrypted); err != nil || encrypted.Version != hostedStateCacheEnvelopeVersion {
		return c.invalidateCorrupt()
	}
	aead, err := c.aead()
	if err != nil {
		return State{}, false, err
	}
	plaintext, err := aead.Open(nil, encrypted.Nonce, encrypted.Ciphertext, c.aad)
	if err != nil {
		return c.invalidateCorrupt()
	}
	var checkpoint stateCheckpoint
	if err := json.Unmarshal(plaintext, &checkpoint); err != nil || checkpoint.MaterializationVersion != hostedStateMaterializationVersion || !validCheckpointState(checkpoint.State) {
		return c.invalidateCorrupt()
	}
	return checkpoint.State, true, nil
}

func (c *hostedStateCache) Save(st State) error {
	if !validCheckpointState(st) {
		return appErr(ErrIntegrity, "refusing to cache an incomplete Magpie state")
	}
	plaintext, err := json.Marshal(stateCheckpoint{MaterializationVersion: hostedStateMaterializationVersion, State: st})
	if err != nil {
		return fmt.Errorf("encode Magpie state checkpoint: %w", err)
	}
	aead, err := c.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("create Magpie state checkpoint nonce: %w", err)
	}
	raw, err := json.Marshal(encryptedStateCheckpoint{
		Version: hostedStateCacheEnvelopeVersion, Nonce: nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, c.aad),
	})
	if err != nil {
		return fmt.Errorf("encode encrypted Magpie state checkpoint: %w", err)
	}

	temporary, err := os.CreateTemp(c.dir, ".hosted-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary Magpie state checkpoint: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary Magpie state checkpoint: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary Magpie state checkpoint: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary Magpie state checkpoint: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Magpie state checkpoint: %w", err)
	}
	if err := os.Rename(temporaryPath, c.path); err != nil {
		return fmt.Errorf("replace Magpie state checkpoint: %w", err)
	}
	removeTemporary = false
	directory, err := os.Open(c.dir)
	if err != nil {
		return fmt.Errorf("open Magpie cache directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync Magpie cache directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Magpie cache directory: %w", closeErr)
	}
	return nil
}

func (c *hostedStateCache) Invalidate() error {
	err := os.Remove(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("invalidate Magpie state checkpoint: %w", err)
	}
	return nil
}

func (c *hostedStateCache) invalidateCorrupt() (State, bool, error) {
	if err := c.Invalidate(); err != nil {
		return State{}, false, err
	}
	return State{}, false, nil
}

func (c *hostedStateCache) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize Magpie state checkpoint cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize Magpie state checkpoint AEAD: %w", err)
	}
	return aead, nil
}

func validCheckpointState(st State) bool {
	return st.Roles != nil && st.Users != nil && st.Accounts != nil && st.JournalEntries != nil &&
		st.Customers != nil && st.Invoices != nil && st.Payouts != nil && st.Notes != nil && st.SourceKeys != nil
}
