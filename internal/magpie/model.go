package magpie

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
	AccountRoleCreditCard              AccountRole = "credit_card"
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

type SourceDocumentStatus string

const (
	SourceDocumentImported SourceDocumentStatus = "imported"
	SourceDocumentOpen     SourceDocumentStatus = "open"
	SourceDocumentPaid     SourceDocumentStatus = "paid"
	SourceDocumentVoid     SourceDocumentStatus = "void"
)

type Customer struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	ExternalRefs []ExternalSourceRef `json:"external_refs,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
	CreatedBy    string              `json:"created_by"`
	UpdatedBy    string              `json:"updated_by"`
}

type InvoiceLineItem struct {
	Description      string `json:"description"`
	RevenueAccountID string `json:"revenue_account_id"`
	Quantity         int64  `json:"quantity"`
	UnitAmountCents  int64  `json:"unit_amount_cents"`
	AmountCents      int64  `json:"amount_cents"`
	TaxAmountCents   int64  `json:"tax_amount_cents,omitempty"`
}

type InvoicePayment struct {
	ID                     string `json:"id"`
	Date                   string `json:"date"`
	AmountCents            int64  `json:"amount_cents"`
	CashAccountID          string `json:"cash_account_id"`
	JournalEntryID         string `json:"journal_entry_id,omitempty"`
	ExternalSource         string `json:"external_source,omitempty"`
	ExternalID             string `json:"external_id,omitempty"`
	PaymentEvidence        string `json:"payment_evidence,omitempty"`
	Reversed               bool   `json:"reversed,omitempty"`
	ReversalDate           string `json:"reversal_date,omitempty"`
	ReversalReason         string `json:"reversal_reason,omitempty"`
	ReversalJournalEntryID string `json:"reversal_journal_entry_id,omitempty"`
}

type ExternalInvoiceImportRequest struct {
	Customer Customer               `json:"customer"`
	Invoice  Invoice                `json:"invoice"`
	Post     bool                   `json:"post"`
	Payment  *InvoicePaymentRequest `json:"payment,omitempty"`
}

type ExternalInvoiceImportResult struct {
	Customer Customer `json:"customer"`
	Invoice  Invoice  `json:"invoice"`
	Posted   bool     `json:"posted"`
	Paid     bool     `json:"paid"`
}

type Invoice struct {
	ID                     string               `json:"id"`
	InvoiceNumber          string               `json:"invoice_number"`
	CustomerID             string               `json:"customer_id"`
	InvoiceDate            string               `json:"invoice_date"`
	DueDate                string               `json:"due_date,omitempty"`
	Status                 SourceDocumentStatus `json:"status"`
	LineItems              []InvoiceLineItem    `json:"line_items"`
	SubtotalCents          int64                `json:"subtotal_cents"`
	TaxAmountCents         int64                `json:"tax_amount_cents"`
	TotalCents             int64                `json:"total_cents"`
	ExternalRefs           []ExternalSourceRef  `json:"external_refs,omitempty"`
	IssuedJournalEntryID   string               `json:"issued_journal_entry_id,omitempty"`
	PaymentJournalEntryIDs []string             `json:"payment_journal_entry_ids,omitempty"`
	Payments               []InvoicePayment     `json:"payments,omitempty"`
	CreatedAt              time.Time            `json:"created_at"`
	UpdatedAt              time.Time            `json:"updated_at"`
	CreatedBy              string               `json:"created_by"`
	UpdatedBy              string               `json:"updated_by"`
}

type Payout struct {
	ID                   string              `json:"id"`
	Date                 string              `json:"date"`
	Description          string              `json:"description,omitempty"`
	SourceAccountID      string              `json:"source_account_id"`
	DestinationAccountID string              `json:"destination_account_id"`
	NetAmountCents       int64               `json:"net_amount_cents"`
	FeeAmountCents       int64               `json:"fee_amount_cents,omitempty"`
	FeeExpenseAccountID  string              `json:"fee_expense_account_id,omitempty"`
	ExternalRefs         []ExternalSourceRef `json:"external_refs,omitempty"`
	JournalEntryIDs      []string            `json:"journal_entry_ids,omitempty"`
	CreatedAt            time.Time           `json:"created_at"`
	UpdatedAt            time.Time           `json:"updated_at"`
	CreatedBy            string              `json:"created_by"`
	UpdatedBy            string              `json:"updated_by"`
}

type BankTransactionStatus string

const (
	BankTransactionStaged BankTransactionStatus = "staged"
	BankTransactionPosted BankTransactionStatus = "posted"
	BankTransactionPaired BankTransactionStatus = "paired"
)

type ReconciliationStatus string

const (
	ReconciliationOpen      ReconciliationStatus = "open"
	ReconciliationCompleted ReconciliationStatus = "completed"
)

// SourceDocumentReference deliberately contains only an opaque identifier and
// a content digest. Provider descriptions, account numbers, and other PII do
// not belong in immutable event metadata.
type SourceDocumentReference struct {
	ID            string `json:"id"`
	ContentSHA256 string `json:"content_sha256"`
}

type BankStatement struct {
	ID                    string                   `json:"id"`
	AccountID             string                   `json:"account_id"`
	PeriodStart           string                   `json:"period_start"`
	PeriodEnd             string                   `json:"period_end"`
	OpeningBalanceCents   int64                    `json:"opening_balance_cents"`
	ClosingBalanceCents   int64                    `json:"closing_balance_cents"`
	Currency              string                   `json:"currency"`
	ExternalRefs          []ExternalSourceRef      `json:"external_refs"`
	SourceDocument        *SourceDocumentReference `json:"source_document,omitempty"`
	OpeningJournalEntryID string                   `json:"opening_journal_entry_id,omitempty"`
	Status                ReconciliationStatus     `json:"status"`
	CompletedAt           time.Time                `json:"completed_at,omitempty"`
	CompletedBy           string                   `json:"completed_by,omitempty"`
	CreatedAt             time.Time                `json:"created_at"`
	CreatedBy             string                   `json:"created_by"`
}

type BankReclassification struct {
	FromAccountID string    `json:"from_account_id"`
	ToAccountID   string    `json:"to_account_id"`
	Reason        string    `json:"reason"`
	JournalID     string    `json:"journal_entry_id"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedBy     string    `json:"created_by"`
}

type BankTransactionReversal struct {
	Reason                  string    `json:"reason"`
	Date                    string    `json:"date"`
	OriginalJournalEntryIDs []string  `json:"original_journal_entry_ids"`
	ReversalJournalEntryIDs []string  `json:"reversal_journal_entry_ids"`
	CreatedAt               time.Time `json:"created_at"`
	CreatedBy               string    `json:"created_by"`
}

type BankTransferHistory struct {
	PairID                 string    `json:"pair_id"`
	OtherTransactionID     string    `json:"other_transaction_id"`
	EconomicDirection      string    `json:"economic_direction"`
	JournalEntryID         string    `json:"journal_entry_id"`
	ReversalJournalEntryID string    `json:"reversal_journal_entry_id,omitempty"`
	ReversalReason         string    `json:"reversal_reason,omitempty"`
	ReversalDate           string    `json:"reversal_date,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	CreatedBy              string    `json:"created_by"`
}

// AmountCents is the signed change to the statement balance. Positive amounts
// increase the balance and negative amounts decrease it. For liability
// accounts (for example, credit cards), an increase is a credit.
type BankTransaction struct {
	ID                    string                    `json:"id"`
	StatementID           string                    `json:"statement_id"`
	AccountID             string                    `json:"account_id"`
	Date                  string                    `json:"date"`
	AmountCents           int64                     `json:"amount_cents"`
	Currency              string                    `json:"currency"`
	Pending               bool                      `json:"pending,omitempty"`
	ExternalRefs          []ExternalSourceRef       `json:"external_refs"`
	SourceDocument        *SourceDocumentReference  `json:"source_document,omitempty"`
	Status                BankTransactionStatus     `json:"status"`
	ClassificationAccount string                    `json:"classification_account_id,omitempty"`
	JournalEntryIDs       []string                  `json:"journal_entry_ids,omitempty"`
	ActiveJournalEntryIDs []string                  `json:"active_journal_entry_ids,omitempty"`
	PostingVersion        int                       `json:"posting_version,omitempty"`
	TransferTransactionID string                    `json:"transfer_transaction_id,omitempty"`
	TransferDirection     string                    `json:"transfer_direction,omitempty"`
	TransferVersion       int                       `json:"transfer_version,omitempty"`
	ReversalReason        string                    `json:"reversal_reason,omitempty"`
	Reclassifications     []BankReclassification    `json:"reclassifications,omitempty"`
	Reversals             []BankTransactionReversal `json:"reversals,omitempty"`
	TransferHistory       []BankTransferHistory     `json:"transfer_history,omitempty"`
	CreatedAt             time.Time                 `json:"created_at"`
	UpdatedAt             time.Time                 `json:"updated_at"`
	CreatedBy             string                    `json:"created_by"`
	UpdatedBy             string                    `json:"updated_by"`
}

type ReconciliationItem struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	Date           string `json:"date,omitempty"`
	AmountCents    int64  `json:"amount_cents,omitempty"`
	BlockingReason string `json:"blocking_reason"`
}

type ReconciliationReport struct {
	StatementID             string               `json:"statement_id"`
	AccountID               string               `json:"account_id"`
	PeriodStart             string               `json:"period_start"`
	PeriodEnd               string               `json:"period_end"`
	Currency                string               `json:"currency"`
	OpeningBalanceCents     int64                `json:"opening_balance_cents"`
	StatementActivityCents  int64                `json:"statement_activity_cents"`
	ClosingBalanceCents     int64                `json:"closing_balance_cents"`
	LedgerBalanceCents      int64                `json:"ledger_balance_cents"`
	DifferenceCents         int64                `json:"difference_cents"`
	ActivityDifferenceCents int64                `json:"activity_difference_cents"`
	UnmatchedItems          []ReconciliationItem `json:"unmatched_items"`
	DuplicateItems          []ReconciliationItem `json:"duplicate_items"`
	PendingItems            []ReconciliationItem `json:"pending_items"`
	OutOfPeriodItems        []ReconciliationItem `json:"out_of_period_items"`
	Blockers                []string             `json:"blockers"`
	CanComplete             bool                 `json:"can_complete"`
	Status                  ReconciliationStatus `json:"status"`
}

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
	Roles            map[string]Role            `json:"roles"`
	Users            map[string]User            `json:"users"`
	Settings         BookSettings               `json:"settings"`
	Accounts         map[string]Account         `json:"accounts"`
	JournalEntries   map[string]JournalEntry    `json:"journal_entries"`
	Customers        map[string]Customer        `json:"customers"`
	Invoices         map[string]Invoice         `json:"invoices"`
	Payouts          map[string]Payout          `json:"payouts"`
	BankStatements   map[string]BankStatement   `json:"bank_statements"`
	BankTransactions map[string]BankTransaction `json:"bank_transactions"`
	Notes            map[string]Note            `json:"notes"`
	SourceKeys       map[string]string          `json:"source_keys"`
	Root             string                     `json:"root,omitempty"`
}

func emptyState() State {
	return State{
		Roles:            map[string]Role{},
		Users:            map[string]User{},
		Settings:         DefaultBookSettings(),
		Accounts:         map[string]Account{},
		JournalEntries:   map[string]JournalEntry{},
		Customers:        map[string]Customer{},
		Invoices:         map[string]Invoice{},
		Payouts:          map[string]Payout{},
		BankStatements:   map[string]BankStatement{},
		BankTransactions: map[string]BankTransaction{},
		Notes:            map[string]Note{},
		SourceKeys:       map[string]string{},
	}
}

type Context struct {
	Actor string
	Role  string
}
