package infobase

import "time"

type Permission string

const (
	PermissionLedgerRead     Permission = "ledger:read"
	PermissionLedgerWrite    Permission = "ledger:write"
	PermissionNotesRead      Permission = "notes:read"
	PermissionNotesWrite     Permission = "notes:write"
	PermissionRBACManage     Permission = "rbac:manage"
	PermissionSnapshot       Permission = "snapshot:create"
	PermissionAuditRead      Permission = "audit:read"
	PermissionAdminRecover   Permission = "admin:recover"
	PermissionSettingsManage Permission = "settings:manage"
	PermissionJournalAdjust  Permission = "journal:adjust"
	PermissionChartManage    Permission = "chart:manage"
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

type AccountRole string

const (
	AccountRoleOperatingCash           AccountRole = "operating_cash"
	AccountRoleBankAccount             AccountRole = "bank_account"
	AccountRoleAccountsReceivable      AccountRole = "accounts_receivable"
	AccountRoleUndepositedFunds        AccountRole = "undeposited_funds"
	AccountRoleInventory               AccountRole = "inventory"
	AccountRoleFixedAsset              AccountRole = "fixed_asset"
	AccountRoleAccumulatedDepreciation AccountRole = "accumulated_depreciation"
	AccountRoleAccountsPayable         AccountRole = "accounts_payable"
	AccountRoleSalesTaxPayable         AccountRole = "sales_tax_payable"
	AccountRolePayrollTaxPayable       AccountRole = "payroll_tax_payable"
	AccountRoleLoanPrincipal           AccountRole = "loan_principal"
	AccountRoleOwnerContribution       AccountRole = "owner_contribution"
	AccountRoleOwnerDraw               AccountRole = "owner_draw"
	AccountRoleRetainedEarnings        AccountRole = "retained_earnings"
	AccountRoleOpeningBalanceEquity    AccountRole = "opening_balance_equity"
	AccountRoleDefaultServiceRevenue   AccountRole = "default_service_revenue"
	AccountRoleDefaultProductRevenue   AccountRole = "default_product_revenue"
	AccountRoleOtherIncome             AccountRole = "other_income"
	AccountRoleDefaultExpense          AccountRole = "default_expense"
	AccountRoleMerchantFeesExpense     AccountRole = "merchant_fees_expense"
	AccountRoleInterestExpense         AccountRole = "interest_expense"
	AccountRolePayrollExpense          AccountRole = "payroll_expense"
	AccountRoleDepreciationExpense     AccountRole = "depreciation_expense"
)

type AccountingBasis string

const (
	AccountingBasisCash         AccountingBasis = "cash"
	AccountingBasisModifiedCash AccountingBasis = "modified_cash"
	AccountingBasisAccrual      AccountingBasis = "accrual"
)

type ModifiedCashPolicy struct {
	RevenueRecognition          string `json:"revenue_recognition"`
	ExpenseRecognition          string `json:"expense_recognition"`
	TrackSalesTaxLiability      bool   `json:"track_sales_tax_liability"`
	TrackPayrollTaxLiabilities  bool   `json:"track_payroll_tax_liabilities"`
	TrackLoanPrincipalLiability bool   `json:"track_loan_principal_liability"`
	CapitalizeFixedAssets       bool   `json:"capitalize_fixed_assets"`
	TrackInventory              bool   `json:"track_inventory"`
	UseAccountsReceivable       bool   `json:"use_accounts_receivable"`
	UseAccountsPayable          bool   `json:"use_accounts_payable"`
}

type BookSettings struct {
	AccountingBasis    AccountingBasis    `json:"accounting_basis"`
	ModifiedCashPolicy ModifiedCashPolicy `json:"modified_cash_policy"`
	UpdatedAt          time.Time          `json:"updated_at,omitempty"`
	UpdatedBy          string             `json:"updated_by,omitempty"`
}

type JournalOrigin string

const (
	JournalOriginWorkflow         JournalOrigin = "workflow"
	JournalOriginManualAdjustment JournalOrigin = "manual_adjustment"
	JournalOriginMigration        JournalOrigin = "migration"
	JournalOriginOpeningBalance   JournalOrigin = "opening_balance"
	JournalOriginSystem           JournalOrigin = "system"
)

type Account struct {
	ID           string              `json:"id"`
	Number       string              `json:"number,omitempty"`
	Name         string              `json:"name"`
	Type         AccountType         `json:"type"`
	Role         AccountRole         `json:"role,omitempty"`
	Sensitivity  string              `json:"sensitivity"`
	ExternalRefs []ExternalSourceRef `json:"external_refs,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	CreatedBy    string              `json:"created_by"`
}

type ExternalSourceRef struct {
	SourceSystem string            `json:"source_system"`
	ExternalID   string            `json:"external_id"`
	ExternalType string            `json:"external_type,omitempty"`
	DisplayName  string            `json:"display_name,omitempty"`
	URL          string            `json:"url,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type Posting struct {
	AccountID string `json:"account_id"`
	Debit     int64  `json:"debit_cents"`
	Credit    int64  `json:"credit_cents"`
	Memo      string `json:"memo,omitempty"`
}

type JournalEntry struct {
	ID                 string            `json:"id"`
	Date               string            `json:"date"`
	Memo               string            `json:"memo"`
	AccountingBasis    AccountingBasis   `json:"accounting_basis,omitempty"`
	Origin             JournalOrigin     `json:"origin,omitempty"`
	Workflow           string            `json:"workflow,omitempty"`
	PostingSemantics   string            `json:"posting_semantics,omitempty"`
	SourceDocumentType string            `json:"source_document_type,omitempty"`
	SourceDocumentID   string            `json:"source_document_id,omitempty"`
	ManualReason       string            `json:"manual_reason,omitempty"`
	Postings           []Posting         `json:"postings"`
	Source             string            `json:"source,omitempty"`
	SourceKey          string            `json:"source_key,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	GeneratedBy        string            `json:"generated_by,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	CreatedBy          string            `json:"created_by"`
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
	Settings       BookSettings            `json:"settings"`
	Accounts       map[string]Account      `json:"accounts"`
	JournalEntries map[string]JournalEntry `json:"journal_entries"`
	Notes          map[string]Note         `json:"notes"`
	SourceKeys     map[string]string       `json:"source_keys"`
	Root           string                  `json:"root,omitempty"`
}

func emptyState() State {
	return State{
		Roles:          map[string]Role{},
		Users:          map[string]User{},
		Settings:       DefaultBookSettings(),
		Accounts:       map[string]Account{},
		JournalEntries: map[string]JournalEntry{},
		Notes:          map[string]Note{},
		SourceKeys:     map[string]string{},
	}
}

type Context struct {
	Actor string
	Role  string
}
