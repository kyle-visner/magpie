package infobase

import (
	"strings"
)

type settingsUpdatePayload struct {
	Settings BookSettings `json:"settings"`
}

func DefaultBookSettings() BookSettings {
	return BookSettings{
		AccountingBasis:    AccountingBasisCash,
		ModifiedCashPolicy: DefaultModifiedCashPolicy(),
	}
}

func DefaultModifiedCashPolicy() ModifiedCashPolicy {
	return ModifiedCashPolicy{
		RevenueRecognition:          "cash_received",
		ExpenseRecognition:          "cash_paid",
		TrackSalesTaxLiability:      true,
		TrackPayrollTaxLiabilities:  true,
		TrackLoanPrincipalLiability: true,
		CapitalizeFixedAssets:       true,
		TrackInventory:              false,
		UseAccountsReceivable:       false,
		UseAccountsPayable:          false,
	}
}

func (s *Store) GetBookSettings(ctx Context) (BookSettings, error) {
	st, err := s.LoadState()
	if err != nil {
		return BookSettings{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerRead); err != nil {
		return BookSettings{}, err
	}
	return st.effectiveSettings(), nil
}

func (s *Store) SetAccountingBasis(ctx Context, basis AccountingBasis) (BookSettings, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return BookSettings{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionSettingsManage); err != nil {
		return BookSettings{}, "", err
	}
	normalized, err := normalizeAccountingBasis(basis)
	if err != nil {
		return BookSettings{}, "", err
	}
	settings := st.effectiveSettings()
	if settings.AccountingBasis == normalized {
		return settings, st.Root, nil
	}
	if len(st.JournalEntries) > 0 {
		return BookSettings{}, "", appErr(ErrValidation, "accounting basis cannot be changed after journal entries exist")
	}
	settings.AccountingBasis = normalized
	settings.ModifiedCashPolicy = DefaultModifiedCashPolicy()
	settings.UpdatedAt = s.now().UTC()
	settings.UpdatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "book.settings", "book:settings", "book settings set", wrapEvent("settings.update", settingsUpdatePayload{Settings: settings}), true)
	return settings, hash, err
}

func (st State) effectiveSettings() BookSettings {
	settings := st.Settings
	if settings.AccountingBasis == "" {
		settings.AccountingBasis = AccountingBasisCash
	}
	if settings.ModifiedCashPolicy == (ModifiedCashPolicy{}) {
		settings.ModifiedCashPolicy = DefaultModifiedCashPolicy()
	}
	return settings
}

func normalizeAccountingBasis(basis AccountingBasis) (AccountingBasis, error) {
	normalized := AccountingBasis(strings.ToLower(strings.TrimSpace(string(basis))))
	switch normalized {
	case AccountingBasisCash, AccountingBasisModifiedCash, AccountingBasisAccrual:
		return normalized, nil
	default:
		return "", appErr(ErrValidation, "invalid accounting basis %q", basis)
	}
}
