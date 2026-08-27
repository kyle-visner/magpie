package magpie

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type accountCreatePayload struct {
	Account Account `json:"account"`
}

type accountUpdatePayload struct {
	Account Account `json:"account"`
}

type journalCreatePayload struct {
	Entry     JournalEntry `json:"entry"`
	SourceKey string       `json:"source_key,omitempty"`
}

type workflowJournalRequest struct {
	Date               string
	Memo               string
	Workflow           string
	PostingSemantics   string
	SourceDocumentType string
	SourceDocumentID   string
	Source             string
	SourceKey          string
	Postings           []Posting
	Metadata           map[string]string
}

func (s *Store) CreateAccount(ctx Context, name string, typ AccountType, sensitivity string) (Account, string, error) {
	return s.CreateAccountWithDetails(ctx, Account{Name: name, Type: typ, Sensitivity: sensitivity})
}

func (s *Store) CreateAccountWithExternalRefs(ctx Context, name string, typ AccountType, sensitivity string, externalRefs []ExternalSourceRef) (Account, string, error) {
	return s.CreateAccountWithDetails(ctx, Account{Name: name, Type: typ, Sensitivity: sensitivity, ExternalRefs: externalRefs})
}

func (s *Store) CreateAccountWithDetails(ctx Context, account Account) (Account, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Account{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Account{}, "", err
	}
	account.ID = ""
	account.Name = strings.TrimSpace(account.Name)
	if account.Name == "" {
		return Account{}, "", appErr(ErrValidation, "account name is required")
	}
	if err := validateAccountType(account.Type); err != nil {
		return Account{}, "", err
	}
	role, err := normalizeAccountRole(account.Role)
	if err != nil {
		return Account{}, "", err
	}
	if role != "" {
		if err := EnsurePermission(st, ctx, PermissionChartManage); err != nil {
			return Account{}, "", err
		}
	}
	if err := validateAccountRoleForType(role, account.Type); err != nil {
		return Account{}, "", err
	}
	if err := ensureAccountRoleAvailable(st, "", role); err != nil {
		return Account{}, "", err
	}
	number, err := normalizeAccountNumber(account.Number)
	if err != nil {
		return Account{}, "", err
	}
	if number != "" {
		if err := ensureAccountNumberAvailable(st, "", number); err != nil {
			return Account{}, "", err
		}
	}
	externalRefs, err := normalizeExternalRefs(account.ExternalRefs)
	if err != nil {
		return Account{}, "", err
	}
	for _, externalRef := range externalRefs {
		key := externalRefKey(externalRef)
		for _, existing := range st.Accounts {
			for _, existingRef := range existing.ExternalRefs {
				if externalRefKey(existingRef) == key {
					return Account{}, "", appErr(ErrConflict, "external ref %q already belongs to account %s", key, existing.ID)
				}
			}
		}
	}
	id := makeID("acct", strings.ToLower(account.Name), string(account.Type))
	if _, exists := st.Accounts[id]; exists {
		return Account{}, "", appErr(ErrConflict, "account already exists: %s", id)
	}
	if account.Sensitivity == "" {
		account.Sensitivity = "internal"
	}
	now := s.now().UTC()
	acct := Account{ID: id, Number: number, Name: account.Name, Type: account.Type, Role: role, Sensitivity: account.Sensitivity, ExternalRefs: externalRefs, CreatedAt: now, CreatedBy: ctx.Actor}
	hash, err := s.appendEventAt(ctx, "ledger.account", id, "ledger account create", wrapEvent("account.create", accountCreatePayload{Account: acct}), st.Root)
	return acct, hash, err
}

func (s *Store) SetAccountNumber(ctx Context, accountID string, number string) (Account, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Account{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Account{}, "", err
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Account{}, "", appErr(ErrValidation, "account id is required")
	}
	account, ok := st.Accounts[accountID]
	if !ok {
		return Account{}, "", appErr(ErrNotFound, "account %s not found", accountID)
	}
	normalized, err := normalizeAccountNumber(number)
	if err != nil {
		return Account{}, "", err
	}
	if normalized == "" {
		return Account{}, "", appErr(ErrValidation, "account number is required")
	}
	if err := ensureAccountNumberAvailable(st, accountID, normalized); err != nil {
		return Account{}, "", err
	}
	account.Number = normalized
	hash, err := s.appendEventAt(ctx, "ledger.account", account.ID, "ledger account number set", wrapEvent("account.update", accountUpdatePayload{Account: account}), st.Root)
	return account, hash, err
}

func (s *Store) SetAccountRole(ctx Context, accountID string, role AccountRole) (Account, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Account{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Account{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionChartManage); err != nil {
		return Account{}, "", err
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Account{}, "", appErr(ErrValidation, "account id is required")
	}
	account, ok := st.Accounts[accountID]
	if !ok {
		return Account{}, "", appErr(ErrNotFound, "account %s not found", accountID)
	}
	normalized, err := normalizeAccountRole(role)
	if err != nil {
		return Account{}, "", err
	}
	if normalized == "" {
		return Account{}, "", appErr(ErrValidation, "account role is required")
	}
	if err := validateAccountRoleForType(normalized, account.Type); err != nil {
		return Account{}, "", err
	}
	if err := ensureAccountRoleAvailable(st, accountID, normalized); err != nil {
		return Account{}, "", err
	}
	account.Role = normalized
	hash, err := s.appendEventAt(ctx, "ledger.account", account.ID, "ledger account role set", wrapEvent("account.update", accountUpdatePayload{Account: account}), st.Root)
	return account, hash, err
}

func (s *Store) SetAccountExternalRef(ctx Context, accountID string, ref ExternalSourceRef) (Account, string, error) {
	return s.setAccountExternalRef(ctx, accountID, ref, "")
}

func (s *Store) SetAccountExternalRefWithRole(ctx Context, accountID string, ref ExternalSourceRef, role AccountRole) (Account, string, error) {
	return s.setAccountExternalRef(ctx, accountID, ref, role)
}

func (s *Store) setAccountExternalRef(ctx Context, accountID string, ref ExternalSourceRef, role AccountRole) (Account, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Account{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Account{}, "", err
	}
	normalizedRole, err := normalizeAccountRole(role)
	if err != nil {
		return Account{}, "", err
	}
	if normalizedRole != "" {
		if err := EnsurePermission(st, ctx, PermissionChartManage); err != nil {
			return Account{}, "", err
		}
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Account{}, "", appErr(ErrValidation, "account id is required")
	}
	account, ok := st.Accounts[accountID]
	if !ok {
		return Account{}, "", appErr(ErrNotFound, "account %s not found", accountID)
	}
	if normalizedRole != "" {
		if err := validateAccountRoleForType(normalizedRole, account.Type); err != nil {
			return Account{}, "", err
		}
		if err := ensureAccountRoleAvailable(st, accountID, normalizedRole); err != nil {
			return Account{}, "", err
		}
		account.Role = normalizedRole
	}
	normalized, empty, err := normalizeExternalRef(ref)
	if err != nil {
		return Account{}, "", err
	}
	if empty {
		return Account{}, "", appErr(ErrValidation, "external ref metadata is required")
	}
	key := externalRefKey(normalized)
	for _, existing := range st.Accounts {
		if existing.ID == accountID {
			continue
		}
		for _, existingRef := range existing.ExternalRefs {
			if externalRefKey(existingRef) == key {
				return Account{}, "", appErr(ErrConflict, "external ref %q already belongs to account %s", key, existing.ID)
			}
		}
	}
	replaced := false
	refs := append([]ExternalSourceRef(nil), account.ExternalRefs...)
	for i, existingRef := range refs {
		if externalRefKey(existingRef) == key {
			refs[i] = normalized
			replaced = true
			break
		}
	}
	if !replaced {
		refs = append(refs, normalized)
	}
	account.ExternalRefs = refs
	hash, err := s.appendEventAt(ctx, "ledger.account", account.ID, "ledger account external-ref set", wrapEvent("account.update", accountUpdatePayload{Account: account}), st.Root)
	return account, hash, err
}

func normalizeExternalRefs(refs []ExternalSourceRef) ([]ExternalSourceRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]ExternalSourceRef, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		normalized, empty, err := normalizeExternalRef(ref)
		if err != nil {
			return nil, err
		}
		if empty {
			continue
		}
		key := externalRefKey(normalized)
		if seen[key] {
			return nil, appErr(ErrConflict, "duplicate external ref %q on account", key)
		}
		seen[key] = true
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func normalizeAccountNumber(number string) (string, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return "", nil
	}
	for _, r := range number {
		if unicode.IsDigit(r) || r == '-' || r == '.' {
			continue
		}
		return "", appErr(ErrValidation, "account number may only contain digits, hyphen, or dot")
	}
	return number, nil
}

func ensureAccountNumberAvailable(st State, currentAccountID string, number string) error {
	for _, existing := range st.Accounts {
		if existing.ID == currentAccountID {
			continue
		}
		if existing.Number == number {
			return appErr(ErrConflict, "account number %q already belongs to account %s", number, existing.ID)
		}
	}
	return nil
}

func normalizeAccountRole(role AccountRole) (AccountRole, error) {
	normalized := AccountRole(strings.ToLower(strings.TrimSpace(string(role))))
	if normalized == "" {
		return "", nil
	}
	if _, ok := accountRoleTypes()[normalized]; !ok {
		return "", appErr(ErrValidation, "invalid account role %q", role)
	}
	return normalized, nil
}

func validateAccountRoleForType(role AccountRole, typ AccountType) error {
	if role == "" {
		return nil
	}
	expected := accountRoleTypes()[role]
	if expected != typ {
		return appErr(ErrValidation, "account role %q requires account type %q", role, expected)
	}
	return nil
}

func ensureAccountRoleAvailable(st State, currentAccountID string, role AccountRole) error {
	if role == "" || !uniqueAccountRoles()[role] {
		return nil
	}
	for _, existing := range st.Accounts {
		if existing.ID == currentAccountID {
			continue
		}
		if existing.Role == role {
			return appErr(ErrConflict, "account role %q already belongs to account %s", role, existing.ID)
		}
	}
	return nil
}

func accountRoleTypes() map[AccountRole]AccountType {
	return map[AccountRole]AccountType{
		AccountRoleOperatingCash:           AccountAsset,
		AccountRoleBankAccount:             AccountAsset,
		AccountRoleTransferClearing:        AccountAsset,
		AccountRoleCreditCard:              AccountLiability,
		AccountRoleAccountsReceivable:      AccountAsset,
		AccountRoleUndepositedFunds:        AccountAsset,
		AccountRoleInventory:               AccountAsset,
		AccountRoleFixedAsset:              AccountAsset,
		AccountRoleAccumulatedDepreciation: AccountAsset,
		AccountRoleAccountsPayable:         AccountLiability,
		AccountRoleSalesTaxPayable:         AccountLiability,
		AccountRolePayrollTaxPayable:       AccountLiability,
		AccountRoleLoanPrincipal:           AccountLiability,
		AccountRoleOwnerContribution:       AccountEquity,
		AccountRoleOwnerDraw:               AccountEquity,
		AccountRoleRetainedEarnings:        AccountEquity,
		AccountRoleOpeningBalanceEquity:    AccountEquity,
		AccountRoleDefaultServiceRevenue:   AccountRevenue,
		AccountRoleDefaultProductRevenue:   AccountRevenue,
		AccountRoleOtherIncome:             AccountRevenue,
		AccountRoleDefaultExpense:          AccountExpense,
		AccountRoleMerchantFeesExpense:     AccountExpense,
		AccountRolePaymentProcessingFees:   AccountExpense,
		AccountRoleProcessorClearing:       AccountAsset,
		AccountRoleInterestExpense:         AccountExpense,
		AccountRolePayrollExpense:          AccountExpense,
		AccountRoleDepreciationExpense:     AccountExpense,
	}
}

func uniqueAccountRoles() map[AccountRole]bool {
	return map[AccountRole]bool{
		AccountRoleOperatingCash:         true,
		AccountRoleTransferClearing:      true,
		AccountRoleAccountsReceivable:    true,
		AccountRoleAccountsPayable:       true,
		AccountRoleSalesTaxPayable:       true,
		AccountRolePayrollTaxPayable:     true,
		AccountRoleRetainedEarnings:      true,
		AccountRoleOpeningBalanceEquity:  true,
		AccountRoleDefaultServiceRevenue: true,
		AccountRoleDefaultProductRevenue: true,
		AccountRoleMerchantFeesExpense:   true,
		AccountRolePaymentProcessingFees: true,
		AccountRoleProcessorClearing:     true,
		AccountRoleInterestExpense:       true,
		AccountRolePayrollExpense:        true,
		AccountRoleDepreciationExpense:   true,
	}
}

func AccountRoleNames() []string {
	roles := make([]string, 0, len(accountRoleTypes()))
	for role := range accountRoleTypes() {
		roles = append(roles, string(role))
	}
	sort.Strings(roles)
	return roles
}

func normalizeExternalRef(ref ExternalSourceRef) (ExternalSourceRef, bool, error) {
	ref.SourceSystem = strings.ToLower(strings.TrimSpace(ref.SourceSystem))
	ref.ExternalID = strings.TrimSpace(ref.ExternalID)
	ref.ExternalType = strings.TrimSpace(ref.ExternalType)
	ref.DisplayName = strings.TrimSpace(ref.DisplayName)
	ref.URL = strings.TrimSpace(ref.URL)
	ref.Metadata = normalizeStringMap(ref.Metadata)
	if ref.SourceSystem == "" && ref.ExternalID == "" && ref.ExternalType == "" && ref.DisplayName == "" && ref.URL == "" && len(ref.Metadata) == 0 {
		return ExternalSourceRef{}, true, nil
	}
	if ref.SourceSystem == "" {
		return ExternalSourceRef{}, false, appErr(ErrValidation, "external ref source system is required when external metadata is provided")
	}
	if ref.ExternalID == "" {
		return ExternalSourceRef{}, false, appErr(ErrValidation, "external ref id is required when external metadata is provided")
	}
	if lastFour := ref.Metadata["last_four"]; lastFour != "" {
		if len(lastFour) != 4 {
			return ExternalSourceRef{}, false, appErr(ErrValidation, "external ref metadata last_four must contain exactly four digits")
		}
		for _, r := range lastFour {
			if !unicode.IsDigit(r) {
				return ExternalSourceRef{}, false, appErr(ErrValidation, "external ref metadata last_four must contain exactly four digits")
			}
		}
	}
	if ref.URL != "" {
		parsed, err := url.Parse(ref.URL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return ExternalSourceRef{}, false, appErr(ErrValidation, "external ref url must be an absolute URL")
		}
		if parsed.Scheme != "https" {
			return ExternalSourceRef{}, false, appErr(ErrValidation, "external ref url must use https")
		}
	}
	return ref, false, nil
}

func normalizeStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range input {
		key := strings.ToLower(strings.TrimSpace(k))
		value := strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func externalRefKey(ref ExternalSourceRef) string {
	return strings.ToLower(strings.TrimSpace(ref.SourceSystem)) + ":" + strings.TrimSpace(ref.ExternalID)
}

func validateAccountType(typ AccountType) error {
	switch typ {
	case AccountAsset, AccountLiability, AccountEquity, AccountRevenue, AccountExpense:
		return nil
	default:
		return appErr(ErrValidation, "invalid account type %q", typ)
	}
}

func (s *Store) CreateJournalEntry(ctx Context, entry JournalEntry) (JournalEntry, string, error) {
	return s.createJournalEntry(ctx, entry, sourceKeyForEntry(entry))
}

func (s *Store) createWorkflowJournalEntry(ctx Context, req workflowJournalRequest) (JournalEntry, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return JournalEntry{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return JournalEntry{}, "", err
	}
	settings := st.effectiveSettings()
	entry := JournalEntry{
		Date:               strings.TrimSpace(req.Date),
		Memo:               strings.TrimSpace(req.Memo),
		AccountingBasis:    settings.AccountingBasis,
		Origin:             JournalOriginWorkflow,
		Workflow:           strings.TrimSpace(req.Workflow),
		PostingSemantics:   strings.TrimSpace(req.PostingSemantics),
		SourceDocumentType: strings.TrimSpace(req.SourceDocumentType),
		SourceDocumentID:   strings.TrimSpace(req.SourceDocumentID),
		Source:             strings.TrimSpace(req.Source),
		SourceKey:          strings.TrimSpace(req.SourceKey),
		Postings:           req.Postings,
		Metadata:           normalizeStringMap(req.Metadata),
		GeneratedBy:        ctx.Actor,
	}
	if entry.Date == "" {
		entry.Date = s.now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", entry.Date); err != nil {
		return JournalEntry{}, "", appErr(ErrValidation, "date must use YYYY-MM-DD")
	}
	if entry.Workflow == "" || entry.PostingSemantics == "" || entry.SourceDocumentType == "" || entry.SourceDocumentID == "" {
		return JournalEntry{}, "", appErr(ErrValidation, "workflow journals require workflow, posting semantics, and source document metadata")
	}
	if entry.Source == "" || entry.SourceKey == "" {
		return JournalEntry{}, "", appErr(ErrValidation, "workflow journals require source and source_key")
	}
	if len(entry.Postings) < 2 {
		return JournalEntry{}, "", appErr(ErrValidation, "journal entry requires at least two postings")
	}
	var debit, credit int64
	for i, p := range entry.Postings {
		if p.AccountID == "" {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d missing account_id", i)
		}
		if _, ok := st.Accounts[p.AccountID]; !ok {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d references unknown account %s", i, p.AccountID)
		}
		if p.Debit < 0 || p.Credit < 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d has negative amount", i)
		}
		if p.Debit > 0 && p.Credit > 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d cannot have both debit and credit", i)
		}
		if p.Debit == 0 && p.Credit == 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d must have a debit or credit", i)
		}
		debit, err = checkedAddCents(debit, p.Debit, "journal debit total")
		if err != nil {
			return JournalEntry{}, "", err
		}
		credit, err = checkedAddCents(credit, p.Credit, "journal credit total")
		if err != nil {
			return JournalEntry{}, "", err
		}
	}
	if debit != credit {
		return JournalEntry{}, "", appErr(ErrValidation, "journal entry must balance: debit=%d credit=%d", debit, credit)
	}
	sourceKey := sourceKeyForEntry(entry)
	entry.ID = makeID("jrnl", string(entry.AccountingBasis), entry.Workflow, entry.Date, entry.Memo, postingFingerprint(entry.Postings), sourceKey)
	if existingID, ok := st.SourceKeys[sourceKey]; ok {
		existing := st.JournalEntries[existingID]
		if journalEquivalent(existing, entry) {
			return existing, st.Root, nil
		}
		return JournalEntry{}, "", appErr(ErrConflict, "source key %q already belongs to journal entry %s", sourceKey, existingID)
	}
	if _, exists := st.JournalEntries[entry.ID]; exists {
		return JournalEntry{}, "", appErr(ErrConflict, "journal entry already exists: %s", entry.ID)
	}
	entry.CreatedAt = s.now().UTC()
	entry.CreatedBy = ctx.Actor
	hash, err := s.appendJournalEntry(ctx, st, entry, sourceKey, "workflow journal create")
	return entry, hash, err
}

func (s *Store) createJournalEntry(ctx Context, entry JournalEntry, sourceKey string) (JournalEntry, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return JournalEntry{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return JournalEntry{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionJournalAdjust); err != nil {
		return JournalEntry{}, "", appErr(ErrPermission, "manual journal creation requires %s; use invoice, bill, bank, or other domain workflows for operating activity", PermissionJournalAdjust)
	}
	entry.Memo = strings.TrimSpace(entry.Memo)
	entry.Source = strings.TrimSpace(entry.Source)
	entry.SourceKey = strings.TrimSpace(entry.SourceKey)
	entry.Workflow = strings.TrimSpace(entry.Workflow)
	entry.PostingSemantics = strings.TrimSpace(entry.PostingSemantics)
	entry.SourceDocumentType = strings.TrimSpace(entry.SourceDocumentType)
	entry.SourceDocumentID = strings.TrimSpace(entry.SourceDocumentID)
	entry.ManualReason = strings.TrimSpace(entry.ManualReason)
	entry.Metadata = normalizeStringMap(entry.Metadata)
	if entry.Date == "" {
		entry.Date = s.now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", entry.Date); err != nil {
		return JournalEntry{}, "", appErr(ErrValidation, "date must use YYYY-MM-DD")
	}
	if len(entry.Postings) < 2 {
		return JournalEntry{}, "", appErr(ErrValidation, "journal entry requires at least two postings")
	}
	settings := st.effectiveSettings()
	if entry.AccountingBasis == "" {
		entry.AccountingBasis = settings.AccountingBasis
	} else {
		basis, err := normalizeAccountingBasis(entry.AccountingBasis)
		if err != nil {
			return JournalEntry{}, "", err
		}
		if basis != settings.AccountingBasis {
			return JournalEntry{}, "", appErr(ErrValidation, "journal accounting basis %q does not match active book basis %q", basis, settings.AccountingBasis)
		}
		entry.AccountingBasis = basis
	}
	origin, err := normalizeJournalOrigin(entry.Origin)
	if err != nil {
		return JournalEntry{}, "", err
	}
	if origin == "" {
		origin = JournalOriginManualAdjustment
	}
	if origin != JournalOriginManualAdjustment {
		return JournalEntry{}, "", appErr(ErrValidation, "ledger journal create only accepts origin %q; generated workflow journals must use canonical workflows", JournalOriginManualAdjustment)
	}
	entry.Origin = origin
	if entry.Workflow != "" {
		return JournalEntry{}, "", appErr(ErrValidation, "manual journal entries cannot set workflow metadata")
	}
	var debit, credit int64
	for i, p := range entry.Postings {
		if p.AccountID == "" {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d missing account_id", i)
		}
		if _, ok := st.Accounts[p.AccountID]; !ok {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d references unknown account %s", i, p.AccountID)
		}
		if p.Debit < 0 || p.Credit < 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d has negative amount", i)
		}
		if p.Debit > 0 && p.Credit > 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d cannot have both debit and credit", i)
		}
		if p.Debit == 0 && p.Credit == 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d must have a debit or credit", i)
		}
		debit, err = checkedAddCents(debit, p.Debit, "journal debit total")
		if err != nil {
			return JournalEntry{}, "", err
		}
		credit, err = checkedAddCents(credit, p.Credit, "journal credit total")
		if err != nil {
			return JournalEntry{}, "", err
		}
	}
	if debit != credit {
		return JournalEntry{}, "", appErr(ErrValidation, "journal entry must balance: debit=%d credit=%d", debit, credit)
	}
	if entry.ManualReason == "" {
		return JournalEntry{}, "", appErr(ErrValidation, "manual journal entries require manual_reason")
	}
	if entry.ID == "" {
		entry.ID = makeID("jrnl", string(entry.AccountingBasis), entry.Date, entry.Memo, postingFingerprint(entry.Postings), sourceKey)
	}
	if sourceKey != "" {
		if existingID, ok := st.SourceKeys[sourceKey]; ok {
			existing := st.JournalEntries[existingID]
			if journalEquivalent(existing, entry) {
				return existing, st.Root, nil
			}
			return JournalEntry{}, "", appErr(ErrConflict, "source key %q already belongs to journal entry %s", sourceKey, existingID)
		}
	}
	if _, exists := st.JournalEntries[entry.ID]; exists {
		return JournalEntry{}, "", appErr(ErrConflict, "journal entry already exists: %s", entry.ID)
	}
	entry.GeneratedBy = ctx.Actor
	entry.CreatedAt = s.now().UTC()
	entry.CreatedBy = ctx.Actor
	hash, err := s.appendJournalEntry(ctx, st, entry, sourceKey, "ledger journal create")
	return entry, hash, err
}

// appendJournalEntry is the single production choke point for all dated
// postings. Domain workflows and privileged manual journals must both pass
// through it before a ledger.journal event can be appended.
func (s *Store) appendJournalEntry(ctx Context, st State, entry JournalEntry, sourceKey, command string) (string, error) {
	if err := ensurePostingDateOpen(st, entry.Date); err != nil {
		return "", err
	}
	return s.appendEventAt(ctx, "ledger.journal", entry.ID, command, wrapEvent("journal.create", journalCreatePayload{Entry: entry, SourceKey: sourceKey}), st.Root)
}

func sourceKeyForEntry(entry JournalEntry) string {
	source := strings.TrimSpace(entry.Source)
	sourceKey := strings.TrimSpace(entry.SourceKey)
	if source == "" || sourceKey == "" {
		return ""
	}
	return source + ":" + sourceKey
}

func journalEquivalent(a, b JournalEntry) bool {
	if a.Date != b.Date ||
		a.Memo != b.Memo ||
		a.AccountingBasis != b.AccountingBasis ||
		a.Origin != b.Origin ||
		a.Workflow != b.Workflow ||
		a.PostingSemantics != b.PostingSemantics ||
		a.SourceDocumentType != b.SourceDocumentType ||
		a.SourceDocumentID != b.SourceDocumentID ||
		a.Source != b.Source ||
		a.SourceKey != b.SourceKey {
		return false
	}
	if len(a.Postings) != len(b.Postings) {
		return false
	}
	for i := range a.Postings {
		if a.Postings[i] != b.Postings[i] {
			return false
		}
	}
	return true
}

func normalizeJournalOrigin(origin JournalOrigin) (JournalOrigin, error) {
	normalized := JournalOrigin(strings.ToLower(strings.TrimSpace(string(origin))))
	switch normalized {
	case "", JournalOriginWorkflow, JournalOriginManualAdjustment, JournalOriginMigration, JournalOriginOpeningBalance, JournalOriginSystem:
		return normalized, nil
	default:
		return "", appErr(ErrValidation, "invalid journal origin %q", origin)
	}
}

func postingFingerprint(postings []Posting) string {
	var b strings.Builder
	for _, p := range postings {
		b.WriteString(p.AccountID)
		b.WriteByte(':')
		b.WriteString(int64String(p.Debit))
		b.WriteByte(':')
		b.WriteString(int64String(p.Credit))
		b.WriteByte(';')
	}
	return b.String()
}

func int64String(v int64) string {
	return strconv.FormatInt(v, 10)
}
