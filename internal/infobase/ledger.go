package infobase

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type accountCreatePayload struct {
	Account Account `json:"account"`
}

type journalCreatePayload struct {
	Entry     JournalEntry `json:"entry"`
	SourceKey string       `json:"source_key,omitempty"`
}

func (s *Store) CreateAccount(ctx Context, name string, typ AccountType, sensitivity string) (Account, string, error) {
	return s.CreateAccountWithExternalRefs(ctx, name, typ, sensitivity, nil)
}

func (s *Store) CreateAccountWithExternalRefs(ctx Context, name string, typ AccountType, sensitivity string, externalRefs []ExternalSourceRef) (Account, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Account{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Account{}, "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Account{}, "", appErr(ErrValidation, "account name is required")
	}
	if err := validateAccountType(typ); err != nil {
		return Account{}, "", err
	}
	externalRefs, err = normalizeExternalRefs(externalRefs)
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
	id := makeID("acct", strings.ToLower(name), string(typ))
	if _, exists := st.Accounts[id]; exists {
		return Account{}, "", appErr(ErrConflict, "account already exists: %s", id)
	}
	if sensitivity == "" {
		sensitivity = "internal"
	}
	now := s.now().UTC()
	acct := Account{ID: id, Name: name, Type: typ, Sensitivity: sensitivity, ExternalRefs: externalRefs, CreatedAt: now, CreatedBy: ctx.Actor}
	hash, err := s.appendEvent(ctx, "ledger.account", id, "ledger account create", wrapEvent("account.create", accountCreatePayload{Account: acct}), true)
	return acct, hash, err
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

func (s *Store) createJournalEntry(ctx Context, entry JournalEntry, sourceKey string) (JournalEntry, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return JournalEntry{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return JournalEntry{}, "", err
	}
	entry.Memo = strings.TrimSpace(entry.Memo)
	if entry.Date == "" {
		entry.Date = s.now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", entry.Date); err != nil {
		return JournalEntry{}, "", appErr(ErrValidation, "date must use YYYY-MM-DD")
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
		debit += p.Debit
		credit += p.Credit
	}
	if debit != credit {
		return JournalEntry{}, "", appErr(ErrValidation, "journal entry must balance: debit=%d credit=%d", debit, credit)
	}
	if entry.ID == "" {
		entry.ID = makeID("jrnl", entry.Date, entry.Memo, postingFingerprint(entry.Postings), sourceKey)
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
	entry.CreatedAt = s.now().UTC()
	entry.CreatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "ledger.journal", entry.ID, "ledger journal create", wrapEvent("journal.create", journalCreatePayload{Entry: entry, SourceKey: sourceKey}), true)
	return entry, hash, err
}

func sourceKeyForEntry(entry JournalEntry) string {
	if entry.Source == "" || entry.SourceKey == "" {
		return ""
	}
	return entry.Source + ":" + entry.SourceKey
}

func journalEquivalent(a, b JournalEntry) bool {
	if a.Date != b.Date || a.Memo != b.Memo || a.Source != b.Source || a.SourceKey != b.SourceKey {
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
