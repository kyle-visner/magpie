package magpie

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kyle-visner/jaybase"
)

type CloseBlocker struct {
	Code     string `json:"code"`
	EntityID string `json:"entity_id,omitempty"`
	Message  string `json:"message"`
}

type ClosePreview struct {
	Through         string          `json:"through"`
	JaybaseRoot     string          `json:"jaybase_root"`
	AccountingBasis AccountingBasis `json:"accounting_basis"`
	Blocked         bool            `json:"blocked"`
	Blockers        []CloseBlocker  `json:"blockers"`
}

type CloseAccount struct {
	ID     string      `json:"id"`
	Number string      `json:"number,omitempty"`
	Name   string      `json:"name"`
	Type   AccountType `json:"type"`
	Role   AccountRole `json:"role,omitempty"`
}

type CloseReportParameters struct {
	TrialBalanceAsOf     string `json:"trial_balance_as_of"`
	ProfitLossFrom       string `json:"profit_loss_from"`
	ProfitLossThrough    string `json:"profit_loss_through"`
	BalanceSheetAsOf     string `json:"balance_sheet_as_of"`
	GeneralLedgerFrom    string `json:"general_ledger_from"`
	GeneralLedgerThrough string `json:"general_ledger_through"`
}

type CloseManifest struct {
	Version              int                   `json:"version"`
	CloseID              string                `json:"close_id"`
	PackageID            string                `json:"package_id"`
	OriginalPackageID    string                `json:"original_package_id"`
	PreviousPackageID    string                `json:"previous_package_id,omitempty"`
	Revision             int                   `json:"revision"`
	PreviousCloseID      string                `json:"previous_close_id,omitempty"`
	CorrectionReason     string                `json:"correction_reason,omitempty"`
	SourceRoot           string                `json:"jaybase_source_root"`
	SnapshotName         string                `json:"snapshot_name"`
	AccountingBasis      AccountingBasis       `json:"accounting_basis"`
	Through              string                `json:"through"`
	Accounts             []CloseAccount        `json:"account_set"`
	Parameters           CloseReportParameters `json:"report_parameters"`
	ReportSHA256         map[string]string     `json:"report_sha256"`
	UnresolvedExceptions []CloseBlocker        `json:"unresolved_exceptions"`
	ClosedAt             time.Time             `json:"closed_at"`
	ClosedBy             string                `json:"closed_by"`
}

type PeriodClose struct {
	ID       string        `json:"id"`
	Through  string        `json:"through"`
	Revision int           `json:"revision"`
	Root     string        `json:"close_root"`
	Manifest CloseManifest `json:"manifest"`
}

type PeriodReopen struct {
	ID         string    `json:"id"`
	CloseID    string    `json:"close_id"`
	Through    string    `json:"through"`
	Reason     string    `json:"reason"`
	ReopenedAt time.Time `json:"reopened_at"`
	ReopenedBy string    `json:"reopened_by"`
	Root       string    `json:"reopen_root"`
}

type periodClosePayload struct {
	Close PeriodClose `json:"close"`
}
type periodReopenPayload struct {
	Reopen PeriodReopen `json:"reopen"`
}

type ClosePackage struct {
	Close PeriodClose
	Files map[string][]byte
}

func (s *Store) PreviewPeriodClose(ctx Context, through string) (ClosePreview, error) {
	st, err := s.LoadState()
	if err != nil {
		return ClosePreview{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionPeriodClose); err != nil {
		return ClosePreview{}, err
	}
	return closePreview(st, through)
}

func closePreview(st State, through string) (ClosePreview, error) {
	if err := validDate("through", through); err != nil {
		return ClosePreview{}, err
	}
	preview := ClosePreview{Through: through, JaybaseRoot: st.Root, AccountingBasis: st.effectiveSettings().AccountingBasis, Blockers: []CloseBlocker{}}
	// Every domain type capable of holding staged financial work must register a
	// checker here. Future bill or other financial models must add a checker
	// before they can claim close coverage.
	for _, check := range domainCloseBlockerChecks {
		preview.Blockers = append(preview.Blockers, check(st, through)...)
	}
	roleOwners := map[AccountRole]string{}
	for _, account := range st.Accounts {
		if err := validateAccountRoleForType(account.Role, account.Type); err != nil {
			preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "invalid_account_role", EntityID: account.ID, Message: err.Error()})
		}
		if account.Role != "" && uniqueAccountRoles()[account.Role] {
			if owner, exists := roleOwners[account.Role]; exists {
				preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "duplicate_account_role", EntityID: account.ID, Message: fmt.Sprintf("account role %s is also assigned to %s", account.Role, owner)})
			} else {
				roleOwners[account.Role] = account.ID
			}
		}
		if account.Role == AccountRoleOperatingCash || account.Role == AccountRoleBankAccount || account.Role == AccountRoleCreditCard {
			reconciledThrough := ""
			for _, ref := range account.ExternalRefs {
				candidate := strings.TrimSpace(ref.Metadata["reconciled_through"])
				if candidate != "" {
					if err := validDate("reconciled_through", candidate); err != nil {
						preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "invalid_reconciliation_date", EntityID: account.ID, Message: err.Error()})
						continue
					}
				}
				if candidate > reconciledThrough {
					reconciledThrough = candidate
				}
			}
			for _, statement := range st.BankStatements {
				if statement.AccountID == account.ID && statement.Status == ReconciliationCompleted && statement.PeriodEnd > reconciledThrough {
					reconciledThrough = statement.PeriodEnd
				}
			}
			if reconciledThrough < through {
				preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "unreconciled_financial_account", EntityID: account.ID, Message: fmt.Sprintf("financial account is reconciled through %q, before close date %s", reconciledThrough, through)})
			}
		}
	}
	settings := st.effectiveSettings()
	if settings.AccountingBasis == AccountingBasisAccrual && hasInvoiceThrough(st, through) {
		if _, ok := roleOwners[AccountRoleAccountsReceivable]; !ok {
			preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "missing_account_role", Message: fmt.Sprintf("accrual invoices require account role %s", AccountRoleAccountsReceivable)})
		}
	}
	if hasTaxInvoiceThrough(st, through) {
		if _, ok := roleOwners[AccountRoleSalesTaxPayable]; !ok {
			preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "missing_account_role", Message: fmt.Sprintf("taxed invoices require account role %s", AccountRoleSalesTaxPayable)})
		}
	}
	for _, entry := range st.JournalEntries {
		if entry.Date > through {
			continue
		}
		if entry.AccountingBasis != "" && entry.AccountingBasis != settings.AccountingBasis {
			preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "accounting_basis_mismatch", EntityID: entry.ID, Message: "journal accounting basis differs from the active book basis"})
		}
		var debit, credit int64
		validTotals := true
		for _, posting := range entry.Postings {
			var err error
			debit, err = checkedAddCents(debit, posting.Debit, "close preview journal debit total")
			if err != nil {
				preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "invalid_journal_amount", EntityID: entry.ID, Message: err.Error()})
				validTotals = false
				break
			}
			credit, err = checkedAddCents(credit, posting.Credit, "close preview journal credit total")
			if err != nil {
				preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "invalid_journal_amount", EntityID: entry.ID, Message: err.Error()})
				validTotals = false
				break
			}
		}
		if validTotals && debit != credit {
			preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "unbalanced_journal", EntityID: entry.ID, Message: fmt.Sprintf("journal debit=%d credit=%d", debit, credit)})
		}
	}
	sort.Slice(preview.Blockers, func(i, j int) bool {
		if preview.Blockers[i].Code != preview.Blockers[j].Code {
			return preview.Blockers[i].Code < preview.Blockers[j].Code
		}
		if preview.Blockers[i].EntityID != preview.Blockers[j].EntityID {
			return preview.Blockers[i].EntityID < preview.Blockers[j].EntityID
		}
		return preview.Blockers[i].Message < preview.Blockers[j].Message
	})
	preview.Blocked = len(preview.Blockers) > 0
	return preview, nil
}

type closeBlockerCheck func(State, string) []CloseBlocker

var domainCloseBlockerChecks = []closeBlockerCheck{
	invoiceCloseBlockers,
	payoutCloseBlockers,
	bankCloseBlockers,
}

func bankCloseBlockers(st State, through string) []CloseBlocker {
	blockers := []CloseBlocker{}
	for _, statement := range st.BankStatements {
		if statement.PeriodEnd <= through && statement.Status != ReconciliationCompleted {
			blockers = append(blockers, CloseBlocker{Code: "unreconciled_bank_statement", EntityID: statement.ID, Message: fmt.Sprintf("bank statement %s through %s is not reconciled", statement.ID, statement.PeriodEnd)})
		}
	}
	for _, transaction := range st.BankTransactions {
		if transaction.Date > through {
			continue
		}
		if transaction.Status == BankTransactionStaged {
			blockers = append(blockers, CloseBlocker{Code: "unposted_source_document", EntityID: transaction.ID, Message: fmt.Sprintf("bank transaction %s is staged but not posted or paired", transaction.ID)})
			continue
		}
		if transaction.Status != BankTransactionPosted && transaction.Status != BankTransactionPaired {
			blockers = append(blockers, CloseBlocker{Code: "unposted_source_document", EntityID: transaction.ID, Message: fmt.Sprintf("bank transaction %s has invalid workflow status %q", transaction.ID, transaction.Status)})
			continue
		}
		if len(transaction.ActiveJournalEntryIDs) == 0 {
			blockers = append(blockers, CloseBlocker{Code: "missing_linked_journal", EntityID: transaction.ID, Message: fmt.Sprintf("bank transaction %s has no active journal references", transaction.ID)})
			continue
		}
		for _, journalID := range transaction.ActiveJournalEntryIDs {
			entry, ok := st.JournalEntries[journalID]
			if !ok || !bankJournalBelongsToTransaction(entry, transaction) {
				blockers = append(blockers, CloseBlocker{Code: "missing_linked_journal", EntityID: transaction.ID, Message: fmt.Sprintf("bank transaction %s active journal %s is missing or unrelated", transaction.ID, journalID)})
				continue
			}
			if entry.Date > through {
				blockers = append(blockers, CloseBlocker{Code: "missing_linked_journal", EntityID: transaction.ID, Message: fmt.Sprintf("bank transaction %s active journal %s is dated after the close boundary", transaction.ID, journalID)})
			}
		}
	}
	return blockers
}

func bankJournalBelongsToTransaction(entry JournalEntry, transaction BankTransaction) bool {
	switch transaction.Status {
	case BankTransactionPosted:
		return entry.SourceDocumentType == "bank_transaction" && entry.SourceDocumentID == transaction.ID
	case BankTransactionPaired:
		return entry.SourceDocumentType == "bank_transfer" &&
			(entry.Metadata["from_transaction_id"] == transaction.ID || entry.Metadata["to_transaction_id"] == transaction.ID)
	default:
		return false
	}
}

func invoiceCloseBlockers(st State, through string) []CloseBlocker {
	blockers := []CloseBlocker{}
	basis := st.effectiveSettings().AccountingBasis
	for _, invoice := range st.Invoices {
		if invoice.InvoiceDate <= through && invoice.Status != SourceDocumentVoid {
			if invoiceIsDraft(invoice.Status) {
				blockers = append(blockers, CloseBlocker{Code: "unposted_source_document", EntityID: invoice.ID, Message: fmt.Sprintf("invoice %s is staged but not posted", invoice.InvoiceNumber)})
			}
			if basis == AccountingBasisAccrual && !invoiceIsDraft(invoice.Status) && invoice.IssuedJournalEntryID == "" {
				blockers = append(blockers, CloseBlocker{Code: "unposted_source_document", EntityID: invoice.ID, Message: fmt.Sprintf("accrual invoice %s has no issuance journal", invoice.InvoiceNumber)})
			}
			if invoice.IssuedJournalEntryID != "" {
				if !linkedJournalExists(st, invoice.IssuedJournalEntryID, "invoice", invoice.ID) {
					blockers = append(blockers, CloseBlocker{Code: "missing_linked_journal", EntityID: invoice.ID, Message: fmt.Sprintf("invoice %s issuance journal %s is missing", invoice.InvoiceNumber, invoice.IssuedJournalEntryID)})
				}
			}
		}
		// Payment and reversal dates are independent accounting dates. The
		// current domain allows them to precede InvoiceDate, so they must be
		// inspected even when the invoice itself is future-dated.
		for _, payment := range invoice.Payments {
			if payment.Date <= through && (payment.JournalEntryID == "" || !linkedJournalExists(st, payment.JournalEntryID, "invoice", invoice.ID)) {
				blockers = append(blockers, CloseBlocker{Code: "unposted_source_document", EntityID: invoice.ID, Message: fmt.Sprintf("invoice %s payment %s has no durable journal", invoice.InvoiceNumber, payment.ID)})
			}
			if payment.Reversed && payment.ReversalDate <= through && (payment.ReversalJournalEntryID == "" || !linkedJournalExists(st, payment.ReversalJournalEntryID, "invoice", invoice.ID)) {
				blockers = append(blockers, CloseBlocker{Code: "unposted_source_document", EntityID: invoice.ID, Message: fmt.Sprintf("invoice %s payment reversal %s has no durable journal", invoice.InvoiceNumber, payment.ID)})
			}
		}
	}
	return blockers
}

func payoutCloseBlockers(st State, through string) []CloseBlocker {
	blockers := []CloseBlocker{}
	for _, payout := range st.Payouts {
		if payout.Date > through {
			continue
		}
		expected := 1
		if payout.FeeAmountCents > 0 {
			expected = 2
		}
		if len(payout.JournalEntryIDs) != expected {
			blockers = append(blockers, CloseBlocker{Code: "unposted_source_document", EntityID: payout.ID, Message: fmt.Sprintf("payout %s has %d of %d required journal references", payout.ID, len(payout.JournalEntryIDs), expected)})
			continue
		}
		missing := []string{}
		for _, journalID := range payout.JournalEntryIDs {
			if !linkedJournalExists(st, journalID, "payout", payout.ID) {
				missing = append(missing, journalID)
			}
		}
		if len(missing) > 0 {
			blockers = append(blockers, CloseBlocker{Code: "missing_linked_journal", EntityID: payout.ID, Message: fmt.Sprintf("payout %s has expected journal-reference count %d, but these references are missing or unrelated: %s", payout.ID, expected, strings.Join(missing, ", "))})
		}
	}
	return blockers
}

func linkedJournalExists(st State, journalID, documentType, documentID string) bool {
	entry, ok := st.JournalEntries[journalID]
	return ok && entry.SourceDocumentType == documentType && entry.SourceDocumentID == documentID
}

func (s *Store) CompletePeriodClose(ctx Context, through string) (PeriodClose, error) {
	st, err := s.LoadState()
	if err != nil {
		return PeriodClose{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionPeriodClose); err != nil {
		return PeriodClose{}, err
	}
	if err := validDate("through", through); err != nil {
		return PeriodClose{}, err
	}
	// A close event and its Jaybase named ref cannot be committed atomically by
	// the current storage contract. Repair every durable close ref before making
	// another close decision; this makes retry safe even after a reopen or a
	// later close was appended following a lost/failed ref response.
	if err := s.repairCloseNamedRefs(st); err != nil {
		return PeriodClose{}, err
	}
	if current, ok := activeCloseForThrough(st, through); ok {
		return current, nil
	}
	if active, ok := latestActiveClose(st); ok && through <= active.Through {
		return PeriodClose{}, appErr(ErrValidation, "close date %s must be after active close date %s", through, active.Through)
	}
	preview, err := closePreview(st, through)
	if err != nil {
		return PeriodClose{}, err
	}
	if preview.Blocked {
		return PeriodClose{}, appErr(ErrValidation, "period close has %d blocker(s); run close preview", len(preview.Blockers))
	}
	revision, previousID, correctionReason := nextCloseRevision(st, through)
	closeID := makeID("close", through, fmt.Sprintf("%d", revision), st.Root)
	packageID := makeID("close-package", closeID)
	originalPackageID, previousPackageID := packageLineage(st, through, packageID)
	snapshotName := fmt.Sprintf("period-close-%s-r%d", through, revision)
	parameters, err := closeReportParameters(st, through)
	if err != nil {
		return PeriodClose{}, err
	}
	files, err := closeReportArtifacts(st, parameters)
	if err != nil {
		return PeriodClose{}, err
	}
	hashes := map[string]string{}
	for name, data := range files {
		sum := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	accounts := make([]CloseAccount, 0, len(st.Accounts))
	for _, account := range sortedAccounts(st) {
		accounts = append(accounts, CloseAccount{ID: account.ID, Number: account.Number, Name: account.Name, Type: account.Type, Role: account.Role})
	}
	closedAt := s.now().UTC()
	close := PeriodClose{ID: closeID, Through: through, Revision: revision, Manifest: CloseManifest{
		Version: 1, CloseID: closeID, PackageID: packageID, OriginalPackageID: originalPackageID, PreviousPackageID: previousPackageID,
		Revision: revision, PreviousCloseID: previousID, CorrectionReason: correctionReason,
		SourceRoot: st.Root, SnapshotName: snapshotName, AccountingBasis: st.effectiveSettings().AccountingBasis,
		Through: through, Accounts: accounts, Parameters: parameters, ReportSHA256: hashes,
		UnresolvedExceptions: []CloseBlocker{}, ClosedAt: closedAt, ClosedBy: ctx.Actor,
	}}
	root, err := s.appendEventAt(ctx, "period.close", close.ID, "period close complete", wrapEvent("period.close.complete", periodClosePayload{Close: close}), st.Root)
	if err != nil {
		return PeriodClose{}, err
	}
	close.Root = root
	if err := s.ensureCloseNamedRef(snapshotName, root); err != nil {
		return PeriodClose{}, err
	}
	return close, nil
}

func (s *Store) ReopenPeriod(ctx Context, through, reason string) (PeriodReopen, error) {
	st, err := s.LoadState()
	if err != nil {
		return PeriodReopen{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionPeriodReopen); err != nil {
		return PeriodReopen{}, err
	}
	if err := validDate("through", through); err != nil {
		return PeriodReopen{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return PeriodReopen{}, appErr(ErrValidation, "reopen reason is required")
	}
	if err := s.repairCloseNamedRefs(st); err != nil {
		return PeriodReopen{}, err
	}
	active, ok := latestActiveClose(st)
	if !ok || active.Through != through {
		return PeriodReopen{}, appErr(ErrValidation, "only the latest active close may be reopened")
	}
	reopenedAt := s.now().UTC()
	reopen := PeriodReopen{ID: makeID("reopen", active.ID, reason, reopenedAt.Format(time.RFC3339Nano)), CloseID: active.ID, Through: through, Reason: reason, ReopenedAt: reopenedAt, ReopenedBy: ctx.Actor}
	root, err := s.appendEventAt(ctx, "period.reopen", reopen.ID, "period reopen", wrapEvent("period.reopen", periodReopenPayload{Reopen: reopen}), st.Root)
	if err != nil {
		return PeriodReopen{}, err
	}
	reopen.Root = root
	return reopen, nil
}

func (s *Store) BuildClosePackage(ctx Context, through string) (ClosePackage, error) {
	current, err := s.LoadState()
	if err != nil {
		return ClosePackage{}, err
	}
	if err := EnsurePermission(current, ctx, PermissionLedgerRead); err != nil {
		return ClosePackage{}, err
	}
	if err := validDate("through", through); err != nil {
		return ClosePackage{}, err
	}
	close, ok := latestCloseForThrough(current, through)
	if !ok {
		return ClosePackage{}, appErr(ErrNotFound, "period close through %s not found", through)
	}
	return s.buildClosePackage(close)
}

// BuildClosePackageByID reproduces a specific historical revision. This keeps
// an originally delivered package addressable after corrected revisions exist.
func (s *Store) BuildClosePackageByID(ctx Context, closeID string) (ClosePackage, error) {
	current, err := s.LoadState()
	if err != nil {
		return ClosePackage{}, err
	}
	if err := EnsurePermission(current, ctx, PermissionLedgerRead); err != nil {
		return ClosePackage{}, err
	}
	closeID = strings.TrimSpace(closeID)
	if closeID == "" {
		return ClosePackage{}, appErr(ErrValidation, "close_id is required")
	}
	close, ok := current.PeriodCloses[closeID]
	if !ok {
		return ClosePackage{}, appErr(ErrNotFound, "period close %s not found", closeID)
	}
	return s.buildClosePackage(close)
}

func (s *Store) buildClosePackage(close PeriodClose) (ClosePackage, error) {
	st, err := s.stateAtRoot(close.Manifest.SourceRoot)
	if err != nil {
		return ClosePackage{}, err
	}
	files, err := closeReportArtifacts(st, close.Manifest.Parameters)
	if err != nil {
		return ClosePackage{}, err
	}
	if err := verifyReportHashes(files, close.Manifest.ReportSHA256); err != nil {
		return ClosePackage{}, err
	}
	manifest, err := json.MarshalIndent(close, "", "  ")
	if err != nil {
		return ClosePackage{}, err
	}
	files["manifest.json"] = append(manifest, '\n')
	return ClosePackage{Close: close, Files: files}, nil
}

func verifyReportHashes(files map[string][]byte, sealed map[string]string) error {
	fileNames := make([]string, 0, len(files))
	for name := range files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	for _, name := range fileNames {
		expected, ok := sealed[name]
		if !ok {
			return appErr(ErrIntegrity, "close package artifact %s is not sealed by the manifest", name)
		}
		sum := sha256.Sum256(files[name])
		if actual := hex.EncodeToString(sum[:]); actual != expected {
			return appErr(ErrIntegrity, "close package artifact %s hash mismatch", name)
		}
	}
	sealedNames := make([]string, 0, len(sealed))
	for name := range sealed {
		sealedNames = append(sealedNames, name)
	}
	sort.Strings(sealedNames)
	for _, name := range sealedNames {
		if _, ok := files[name]; !ok {
			return appErr(ErrIntegrity, "sealed close package artifact %s is missing", name)
		}
	}
	return nil
}

func closeReportParameters(st State, through string) (CloseReportParameters, error) {
	parsedThrough, err := time.Parse("2006-01-02", through)
	if err != nil {
		return CloseReportParameters{}, appErr(ErrValidation, "through must use YYYY-MM-DD")
	}
	from := through
	if previous, ok := latestActiveCloseBefore(st, through); ok {
		parsedPrevious, err := time.Parse("2006-01-02", previous.Through)
		if err != nil {
			return CloseReportParameters{}, appErr(ErrIntegrity, "previous close date %q is invalid", previous.Through)
		}
		from = parsedPrevious.AddDate(0, 0, 1).Format("2006-01-02")
	} else {
		for _, entry := range st.JournalEntries {
			if entry.Date <= through && (from == through || entry.Date < from) {
				from = entry.Date
			}
		}
	}
	if from > parsedThrough.Format("2006-01-02") {
		return CloseReportParameters{}, appErr(ErrIntegrity, "close report start %s is after through date %s", from, through)
	}
	return CloseReportParameters{TrialBalanceAsOf: through, ProfitLossFrom: from, ProfitLossThrough: through, BalanceSheetAsOf: through, GeneralLedgerFrom: from, GeneralLedgerThrough: through}, nil
}

func closeReportArtifacts(st State, parameters CloseReportParameters) (map[string][]byte, error) {
	if err := validDate("trial_balance_as_of", parameters.TrialBalanceAsOf); err != nil {
		return nil, err
	}
	if err := validRange(parameters.ProfitLossFrom, parameters.ProfitLossThrough); err != nil {
		return nil, err
	}
	if err := validDate("balance_sheet_as_of", parameters.BalanceSheetAsOf); err != nil {
		return nil, err
	}
	if err := validRange(parameters.GeneralLedgerFrom, parameters.GeneralLedgerThrough); err != nil {
		return nil, err
	}
	tb, err := trialBalance(st, parameters.TrialBalanceAsOf)
	if err != nil {
		return nil, err
	}
	pl, err := profitLoss(st, parameters.ProfitLossFrom, parameters.ProfitLossThrough)
	if err != nil {
		return nil, err
	}
	bs, err := balanceSheet(st, parameters.BalanceSheetAsOf)
	if err != nil {
		return nil, err
	}
	gl, err := generalLedger(st, parameters.GeneralLedgerFrom, parameters.GeneralLedgerThrough)
	if err != nil {
		return nil, err
	}
	reports := []struct {
		name  string
		value any
	}{{"trial-balance", tb}, {"profit-loss", pl}, {"balance-sheet", bs}, {"general-ledger", gl}}
	files := map[string][]byte{}
	for _, report := range reports {
		jsonData, err := CanonicalJSON(report.value)
		if err != nil {
			return nil, err
		}
		files[report.name+".json"] = append(jsonData, '\n')
		csvData, err := ReportCSV(report.value)
		if err != nil {
			return nil, err
		}
		files[report.name+".csv"] = csvData
	}
	return files, nil
}

func ensurePostingDateOpen(st State, date string) error {
	if err := validDate("posting date", date); err != nil {
		return err
	}
	if close, ok := latestActiveClose(st); ok && date <= close.Through {
		return appErr(ErrValidation, "posting date %s is in closed period through %s; privileged reopen is required", date, close.Through)
	}
	return nil
}

func latestActiveClose(st State) (PeriodClose, bool) {
	reopened := map[string]bool{}
	for _, reopen := range st.PeriodReopens {
		reopened[reopen.CloseID] = true
	}
	var best PeriodClose
	found := false
	for _, close := range st.PeriodCloses {
		if reopened[close.ID] {
			continue
		}
		if !found || closeAfter(close, best) {
			best, found = close, true
		}
	}
	return best, found
}

func activeCloseForThrough(st State, through string) (PeriodClose, bool) {
	close, ok := latestActiveClose(st)
	return close, ok && close.Through == through
}

func latestCloseForThrough(st State, through string) (PeriodClose, bool) {
	var best PeriodClose
	found := false
	for _, close := range st.PeriodCloses {
		if close.Through == through && (!found || closeAfter(close, best)) {
			best, found = close, true
		}
	}
	return best, found
}

func latestActiveCloseBefore(st State, through string) (PeriodClose, bool) {
	reopened := map[string]bool{}
	for _, reopen := range st.PeriodReopens {
		reopened[reopen.CloseID] = true
	}
	var best PeriodClose
	found := false
	for _, close := range st.PeriodCloses {
		if reopened[close.ID] || close.Through >= through {
			continue
		}
		if !found || closeAfter(close, best) {
			best, found = close, true
		}
	}
	return best, found
}

// closeAfter is a total ordering. Normal closes are serialized by Jaybase CAS,
// so a revision tie cannot be produced through CompletePeriodClose. The ID tie
// breaker keeps replay deterministic if a hand-crafted or future event violates
// that invariant.
func closeAfter(left, right PeriodClose) bool {
	if left.Through != right.Through {
		return left.Through > right.Through
	}
	if left.Revision != right.Revision {
		return left.Revision > right.Revision
	}
	return left.ID > right.ID
}

func packageLineage(st State, through, newPackageID string) (string, string) {
	previous, ok := latestCloseForThrough(st, through)
	if !ok {
		return newPackageID, ""
	}
	original := previous.Manifest.OriginalPackageID
	if original == "" {
		original = previous.Manifest.PackageID
	}
	if original == "" {
		original = previous.ID
	}
	previousPackage := previous.Manifest.PackageID
	if previousPackage == "" {
		previousPackage = previous.ID
	}
	return original, previousPackage
}

func (s *Store) repairCloseNamedRefs(st State) error {
	closes := make([]PeriodClose, 0, len(st.PeriodCloses))
	for _, close := range st.PeriodCloses {
		closes = append(closes, close)
	}
	sort.Slice(closes, func(i, j int) bool {
		if closes[i].Through != closes[j].Through {
			return closes[i].Through < closes[j].Through
		}
		if closes[i].Revision != closes[j].Revision {
			return closes[i].Revision < closes[j].Revision
		}
		return closes[i].ID < closes[j].ID
	})
	for _, close := range closes {
		if close.Manifest.SnapshotName == "" || close.Root == "" {
			return appErr(ErrIntegrity, "period close %s is missing snapshot provenance", close.ID)
		}
		if err := s.ensureCloseNamedRef(close.Manifest.SnapshotName, close.Root); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureCloseNamedRef(name, root string) error {
	current, err := s.db.NamedRef(name)
	if err == nil {
		if current != root {
			return appErr(ErrIntegrity, "period close ref %s points to %s, expected %s", name, current, root)
		}
		return nil
	}
	var dbErr *jaybase.AppError
	if !errors.As(err, &dbErr) || dbErr.Code != jaybase.ErrNotFound {
		return storageError(err)
	}
	if err := s.db.WriteNamedRefAt(name, root, ""); err != nil {
		writeErr := storageError(err)
		current, readErr := s.db.NamedRef(name)
		if readErr == nil {
			if current == root {
				return nil
			}
			return appErr(ErrIntegrity, "period close ref %s concurrently changed to %s, expected %s", name, current, root)
		}
		return writeErr
	}
	return nil
}

func nextCloseRevision(st State, through string) (int, string, string) {
	previous, ok := latestCloseForThrough(st, through)
	if !ok {
		return 1, "", ""
	}
	reason := ""
	for _, reopen := range st.PeriodReopens {
		if reopen.CloseID == previous.ID {
			reason = reopen.Reason
		}
	}
	return previous.Revision + 1, previous.ID, reason
}

func hasInvoiceThrough(st State, through string) bool {
	for _, invoice := range st.Invoices {
		if invoice.InvoiceDate <= through && invoice.Status != SourceDocumentVoid {
			return true
		}
	}
	return false
}

func hasTaxInvoiceThrough(st State, through string) bool {
	for _, invoice := range st.Invoices {
		if invoice.InvoiceDate <= through && invoice.Status != SourceDocumentVoid && invoice.TaxAmountCents > 0 {
			return true
		}
	}
	return false
}
