package infobase

import "time"

type Permission string

const (
	PermissionLedgerRead   Permission = "ledger:read"
	PermissionLedgerWrite  Permission = "ledger:write"
	PermissionNotesRead    Permission = "notes:read"
	PermissionNotesWrite   Permission = "notes:write"
	PermissionRBACManage   Permission = "rbac:manage"
	PermissionImportQB     Permission = "import:quickbooks"
	PermissionSnapshot     Permission = "snapshot:create"
	PermissionAuditRead    Permission = "audit:read"
	PermissionAdminRecover Permission = "admin:recover"
)

type Role struct {
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
}

type User struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

type AccountType string

const (
	AccountAsset     AccountType = "asset"
	AccountLiability AccountType = "liability"
	AccountEquity    AccountType = "equity"
	AccountRevenue   AccountType = "revenue"
	AccountExpense   AccountType = "expense"
)

type Account struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Type        AccountType `json:"type"`
	Sensitivity string      `json:"sensitivity"`
	CreatedAt   time.Time   `json:"created_at"`
	CreatedBy   string      `json:"created_by"`
}

type Posting struct {
	AccountID string `json:"account_id"`
	Debit     int64  `json:"debit_cents"`
	Credit    int64  `json:"credit_cents"`
	Memo      string `json:"memo,omitempty"`
}

type JournalEntry struct {
	ID        string            `json:"id"`
	Date      string            `json:"date"`
	Memo      string            `json:"memo"`
	Postings  []Posting         `json:"postings"`
	Source    string            `json:"source,omitempty"`
	SourceKey string            `json:"source_key,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	CreatedBy string            `json:"created_by"`
}

type Note struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	Sensitivity string            `json:"sensitivity"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CreatedBy   string            `json:"created_by"`
	UpdatedBy   string            `json:"updated_by"`
}

type State struct {
	Roles          map[string]Role         `json:"roles"`
	Users          map[string]User         `json:"users"`
	Accounts       map[string]Account      `json:"accounts"`
	JournalEntries map[string]JournalEntry `json:"journal_entries"`
	Notes          map[string]Note         `json:"notes"`
	ImportKeys     map[string]string       `json:"import_keys"`
	Root           string                  `json:"root,omitempty"`
}

func emptyState() State {
	return State{
		Roles:          map[string]Role{},
		Users:          map[string]User{},
		Accounts:       map[string]Account{},
		JournalEntries: map[string]JournalEntry{},
		Notes:          map[string]Note{},
		ImportKeys:     map[string]string{},
	}
}

type Context struct {
	Actor string
	Role  string
}
