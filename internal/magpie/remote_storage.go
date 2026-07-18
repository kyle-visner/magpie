package magpie

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kyle-visner/jaybase"
)

const (
	maxRemoteResponseBytes = 64 << 20
	remoteEventPageSize    = 50
)

type remoteStorageBackend struct {
	baseURL string
	token   string
	client  *http.Client
}

type remoteEventPage struct {
	Events  []jaybase.Node `json:"events"`
	Root    string         `json:"root"`
	HasMore bool           `json:"has_more"`
}

// OpenRemoteStore connects Magpie to a hosted Jaybase HTTP API. The bearer
// token is deliberately supplied separately from the URL so it never appears
// in request paths or persisted store data.
func OpenRemoteStore(rawURL, token string) (*Store, error) {
	return openRemoteStore(rawURL, token, &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	})
}

func openRemoteStore(rawURL, token string, client *http.Client) (*Store, error) {
	baseURL, err := normalizeJaybaseURL(rawURL)
	if err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, appErr(ErrValidation, "JAYBASE_TOKEN is required when JAYBASE_URL is configured")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return newStore(&remoteStorageBackend{baseURL: baseURL, token: token, client: client}), nil
}

func normalizeJaybaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", appErr(ErrValidation, "JAYBASE_URL must be an absolute HTTPS origin")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", appErr(ErrValidation, "JAYBASE_URL must be an origin without credentials, a path, query, or fragment")
	}
	hostname := parsed.Hostname()
	local := strings.EqualFold(hostname, "localhost")
	if ip := net.ParseIP(hostname); ip != nil && ip.IsLoopback() {
		local = true
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && local) {
		return "", appErr(ErrValidation, "JAYBASE_URL must use HTTPS except for loopback development")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (b *remoteStorageBackend) Dir() string {
	return b.baseURL
}

func (b *remoteStorageBackend) Close() error {
	return nil
}

func (b *remoteStorageBackend) CurrentRoot() (string, error) {
	var response struct {
		Root string `json:"root"`
	}
	if err := b.doJSON(http.MethodGet, "/v1/root", nil, "", &response); err != nil {
		return "", err
	}
	return response.Root, nil
}

func (b *remoteStorageBackend) AppendAt(ctx jaybase.Context, options jaybase.AppendOptions, expectedRoot string) (string, error) {
	request := struct {
		Type         string `json:"type"`
		EntityID     string `json:"entity_id,omitempty"`
		Command      string `json:"command"`
		Payload      any    `json:"payload"`
		ExpectedRoot string `json:"expected_root"`
	}{
		Type: options.Type, EntityID: options.EntityID, Command: options.Command,
		Payload: options.Payload, ExpectedRoot: expectedRoot,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(ctx.Actor+"\x00"), body...))
	idempotencyKey := "magpie-" + hex.EncodeToString(sum[:])
	var response struct {
		Hash string `json:"hash"`
	}
	if err := b.doJSON(http.MethodPost, "/v1/events", body, idempotencyKey, &response); err != nil {
		return "", err
	}
	if response.Hash == "" {
		return "", appErr(ErrValidation, "Jaybase append response did not include a hash")
	}
	return response.Hash, nil
}

func (b *remoteStorageBackend) NodesFromRoot(root string) ([]jaybase.Node, error) {
	if root == "" {
		return nil, nil
	}
	nodes, found, err := b.readEvents(root, true)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &jaybase.AppError{Code: jaybase.ErrNotFound, Message: fmt.Sprintf("root %s was not found in the current Jaybase history", root)}
	}
	return nodes, nil
}

func (b *remoteStorageBackend) AuditLog() ([]jaybase.Node, error) {
	nodes, _, err := b.readEvents("", false)
	return nodes, err
}

func (b *remoteStorageBackend) readEvents(stopAt string, includePayload bool) ([]jaybase.Node, bool, error) {
	nodes := make([]jaybase.Node, 0)
	after := ""
	for {
		query := url.Values{"limit": {strconv.Itoa(remoteEventPageSize)}}
		if includePayload {
			query.Set("include_payload", "true")
		}
		if after != "" {
			query.Set("after", after)
		}
		var page remoteEventPage
		if err := b.doJSON(http.MethodGet, "/v1/events?"+query.Encode(), nil, "", &page); err != nil {
			return nil, false, err
		}
		for _, node := range page.Events {
			nodes = append(nodes, node)
			if stopAt != "" && node.Hash == stopAt {
				return nodes, true, nil
			}
		}
		if !page.HasMore {
			return nodes, stopAt == "", nil
		}
		if len(page.Events) == 0 {
			return nil, false, appErr(ErrValidation, "Jaybase returned has_more without an event cursor")
		}
		after = page.Events[len(page.Events)-1].Hash
	}
}

func (b *remoteStorageBackend) NodePayload(node jaybase.Node) ([]byte, error) {
	if len(node.Payload) == 0 {
		return nil, appErr(ErrValidation, "Jaybase event %s did not include its decrypted payload", node.Hash)
	}
	return append([]byte(nil), node.Payload...), nil
}

func (b *remoteStorageBackend) WriteNamedRef(name, root string) error {
	expectedRoot := ""
	var current struct {
		Root string `json:"root"`
	}
	path := "/v1/refs/" + url.PathEscape(name)
	if err := b.doJSON(http.MethodGet, path, nil, "", &current); err != nil {
		var appError *jaybase.AppError
		if !errors.As(err, &appError) || appError.Code != jaybase.ErrNotFound {
			return err
		}
	} else {
		expectedRoot = current.Root
	}
	body, err := json.Marshal(map[string]string{"root": root, "expected_root": expectedRoot})
	if err != nil {
		return err
	}
	return b.doJSON(http.MethodPut, path, body, "", nil)
}

func (b *remoteStorageBackend) NodePath(string) string {
	return ""
}

func (b *remoteStorageBackend) doJSON(method, path string, body []byte, idempotencyKey string, result any) error {
	for attempt := 0; attempt < 3; attempt++ {
		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(body)
		}
		request, err := http.NewRequest(method, b.baseURL+path, requestBody)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+b.token)
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			request.Header.Set("Idempotency-Key", idempotencyKey)
		}

		response, err := b.client.Do(request)
		if err != nil {
			if attempt < 2 {
				time.Sleep(time.Duration(100*(1<<attempt)) * time.Millisecond)
				continue
			}
			return fmt.Errorf("Jaybase %s %s failed: %w", method, path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxRemoteResponseBytes+1))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read Jaybase response: %w", readErr)
		}
		if len(data) > maxRemoteResponseBytes {
			return appErr(ErrValidation, "Jaybase response exceeded %s bytes", strconv.Itoa(maxRemoteResponseBytes))
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			remoteError := decodeRemoteError(response.StatusCode, data)
			if attempt < 2 && retryableRemoteError(response.StatusCode, remoteError) {
				time.Sleep(time.Duration(100*(1<<attempt)) * time.Millisecond)
				continue
			}
			return remoteError
		}
		if result != nil && len(data) > 0 {
			if err := json.Unmarshal(data, result); err != nil {
				return fmt.Errorf("decode Jaybase response: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("Jaybase request failed")
}

func retryableRemoteError(status int, err error) bool {
	var appError *jaybase.AppError
	if errors.As(err, &appError) {
		return appError.Code == jaybase.ErrorCode(ErrInternal)
	}
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func decodeRemoteError(status int, data []byte) error {
	var response struct {
		Error jaybase.AppError `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err == nil && response.Error.Code != "" {
		return &response.Error
	}
	code := jaybase.ErrValidation
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		code = jaybase.ErrPermission
	case http.StatusNotFound:
		code = jaybase.ErrNotFound
	case http.StatusConflict:
		code = jaybase.ErrConflict
	case http.StatusInsufficientStorage:
		code = jaybase.ErrCapacity
	default:
		if status >= 500 {
			code = jaybase.ErrorCode(ErrInternal)
		}
	}
	return &jaybase.AppError{Code: code, Message: fmt.Sprintf("Jaybase returned HTTP %d", status)}
}
