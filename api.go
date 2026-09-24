package magpie

import intern "magpie/internal/magpie"

// Public aliases for the host and other AGPL integrators. The domain engine
// stays in internal/magpie; this file is the supported import surface.

type (
	Store                 = intern.Store
	Context               = intern.Context
	Customer              = intern.Customer
	Invoice               = intern.Invoice
	InvoiceLineItem       = intern.InvoiceLineItem
	InvoicePayment        = intern.InvoicePayment
	InvoicePaymentRequest = intern.InvoicePaymentRequest
	Account               = intern.Account
	AccountType           = intern.AccountType
	AccountRole           = intern.AccountRole
	AccountingBasis       = intern.AccountingBasis
	State                 = intern.State
	Permission            = intern.Permission
)

const (
	ActorOwner = "owner"

	AccountAsset     = intern.AccountAsset
	AccountRevenue   = intern.AccountRevenue
	AccountLiability = intern.AccountLiability

	AccountRoleOperatingCash         = intern.AccountRoleOperatingCash
	AccountRoleAccountsReceivable    = intern.AccountRoleAccountsReceivable
	AccountRoleDefaultServiceRevenue = intern.AccountRoleDefaultServiceRevenue

	AccountingBasisAccrual = intern.AccountingBasisAccrual
	AccountingBasisCash    = intern.AccountingBasisCash

	PermissionLedgerRead  = intern.PermissionLedgerRead
	PermissionLedgerWrite = intern.PermissionLedgerWrite
)

func OpenStore(dir string) (*Store, error) {
	return intern.OpenStore(dir)
}

func OpenRemoteStore(jaybaseURL, token string) (*Store, error) {
	return intern.OpenRemoteStore(jaybaseURL, token)
}

func EnsurePermission(st State, ctx Context, permission Permission) error {
	return intern.EnsurePermission(st, ctx, permission)
}

type CatalogEntry = intern.CatalogEntry

const (
	EventNamespace              = intern.EventNamespace
	RefPattern                  = intern.RefPattern
	LegacyPeriodCloseRefPattern = intern.LegacyPeriodCloseRefPattern
)

// WriterCatalog is the exact type and command list a host installs for new
// Magpie writes. Every type is in EventNamespace, so one allow.types pattern
// ("magpie.*") covers them. Magpie does not install the catalog.
func WriterCatalog() []CatalogEntry {
	return intern.WriterCatalog()
}

// LegacyTypeAliases maps historical event types to the current catalog type.
// Replay reads the keys. New appends use the values.
func LegacyTypeAliases() map[string]string {
	return intern.LegacyTypeAliases()
}
