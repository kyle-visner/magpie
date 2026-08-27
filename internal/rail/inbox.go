package rail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type InboxItem struct {
	ID        string          `json:"id"`
	Received  string          `json:"received"`
	Reason    string          `json:"reason"`
	EventType string          `json:"event_type,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func defaultInboxDir(cfg Config) string {
	if strings.TrimSpace(cfg.InboxDir) != "" {
		return cfg.InboxDir
	}
	if strings.TrimSpace(cfg.StoreDir) != "" {
		return filepath.Join(cfg.StoreDir, "rail-inbox")
	}
	return "rail-inbox"
}

func RecordInbox(cfg Config, item InboxItem) error {
	dir := defaultInboxDir(cfg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if item.ID == "" {
		item.ID = time.Now().UTC().Format("20060102T150405.000000000")
	}
	if item.Received == "" {
		item.Received = time.Now().UTC().Format(time.RFC3339)
	}
	buf, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, item.ID+".json"), buf, 0o600)
}

func ListInbox(cfg Config, since string) ([]InboxItem, error) {
	dir := defaultInboxDir(cfg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []InboxItem{}, nil
		}
		return nil, err
	}
	items := make([]InboxItem, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var item InboxItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		if since != "" && item.Received < since && item.ID < since {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Received < items[j].Received })
	return items, nil
}
