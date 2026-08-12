package magpie

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
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
	preview := ClosePreview{Through: through, JaybaseRoot: st.Root, AccountingBasis: st.effectiveSettings().AccountingBasis}
	for _, invoice := range st.Invoices {
		if invoice.InvoiceDate <= through && invoice.Status == SourceDocumentImported {
			preview.Blockers = append(preview.Blockers, CloseBlocker{Code: "unposted_source_document", EntityID: invoice.ID, Message: fmt.Sprintf("invoice %s is staged but not posted", invoice.InvoiceNumber)})
		}
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
		if account.Role == AccountRoleOperatingCash || account.Role == AccountRoleBankAccount {
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
		for _, posting := range entry.Postings {
			debit += posting.Debit
			credit += posting.Credit
		}
		if debit != credit {
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
	if current, ok := activeCloseForThrough(st, through); ok {
		if err := s.writeNamedRef(current.Manifest.SnapshotName, current.Root); err != nil {
			return PeriodClose{}, err
		}
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
	snapshotName := fmt.Sprintf("period-close-%s-r%d", through, revision)
	files, parameters, err := closeReportArtifacts(st, through)
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
		Version: 1, CloseID: closeID, Revision: revision, PreviousCloseID: previousID, CorrectionReason: correctionReason,
		SourceRoot: st.Root, SnapshotName: snapshotName, AccountingBasis: st.effectiveSettings().AccountingBasis,
		Through: through, Accounts: accounts, Parameters: parameters, ReportSHA256: hashes,
		UnresolvedExceptions: []CloseBlocker{}, ClosedAt: closedAt, ClosedBy: ctx.Actor,
	}}
	root, err := s.appendEventAt(ctx, "period.close", close.ID, "period close complete", wrapEvent("period.close.complete", periodClosePayload{Close: close}), st.Root)
	if err != nil {
		return PeriodClose{}, err
	}
	close.Root = root
	if err := s.writeNamedRef(snapshotName, root); err != nil {
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
	close, ok := latestCloseForThrough(current, through)
	if !ok {
		return ClosePackage{}, appErr(ErrNotFound, "period close through %s not found", through)
	}
	st, err := s.stateAtRoot(close.Manifest.SourceRoot)
	if err != nil {
		return ClosePackage{}, err
	}
	files, _, err := closeReportArtifacts(st, through)
	if err != nil {
		return ClosePackage{}, err
	}
	for name, expected := range close.Manifest.ReportSHA256 {
		data, ok := files[name]
		if !ok {
			return ClosePackage{}, appErr(ErrIntegrity, "close package artifact %s is missing", name)
		}
		sum := sha256.Sum256(data)
		if actual := hex.EncodeToString(sum[:]); actual != expected {
			return ClosePackage{}, appErr(ErrIntegrity, "close package artifact %s hash mismatch", name)
		}
	}
	manifest, err := json.MarshalIndent(close, "", "  ")
	if err != nil {
		return ClosePackage{}, err
	}
	files["manifest.json"] = append(manifest, '\n')
	return ClosePackage{Close: close, Files: files}, nil
}

func closeReportArtifacts(st State, through string) (map[string][]byte, CloseReportParameters, error) {
	parsed, _ := time.Parse("2006-01-02", through)
	from := time.Date(parsed.Year(), parsed.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	parameters := CloseReportParameters{TrialBalanceAsOf: through, ProfitLossFrom: from, ProfitLossThrough: through, BalanceSheetAsOf: through, GeneralLedgerFrom: from, GeneralLedgerThrough: through}
	tb, err := trialBalance(st, through)
	if err != nil {
		return nil, parameters, err
	}
	pl, err := profitLoss(st, from, through)
	if err != nil {
		return nil, parameters, err
	}
	bs, err := balanceSheet(st, through)
	if err != nil {
		return nil, parameters, err
	}
	gl, err := generalLedger(st, from, through)
	if err != nil {
		return nil, parameters, err
	}
	reports := []struct {
		name  string
		value any
	}{{"trial-balance", tb}, {"profit-loss", pl}, {"balance-sheet", bs}, {"general-ledger", gl}}
	files := map[string][]byte{}
	for _, report := range reports {
		jsonData, err := CanonicalJSON(report.value)
		if err != nil {
			return nil, parameters, err
		}
		files[report.name+".json"] = append(jsonData, '\n')
		csvData, err := ReportCSV(report.value)
		if err != nil {
			return nil, parameters, err
		}
		files[report.name+".csv"] = csvData
	}
	return files, parameters, nil
}

func ensurePostingDateOpen(st State, date string) error {
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
		if !found || close.Through > best.Through || (close.Through == best.Through && close.Revision > best.Revision) {
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
		if close.Through == through && (!found || close.Revision > best.Revision) {
			best, found = close, true
		}
	}
	return best, found
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
