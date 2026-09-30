package magpie

import intern "github.com/kyle-visner/magpie/internal/magpie"

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
	Book                  = intern.Book
	Tool                  = intern.Tool
	AppError              = intern.AppError
	ErrorCode             = intern.ErrorCode
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

// NewBook binds a store to the acting identity. A host that serves Magpie as
// MCP tools calls Book.Invoke with the tool name and its JSON arguments, the
// same path the magpie mcp command uses.
func NewBook(store *Store, ctx Context) *Book {
	return intern.NewBook(store, ctx)
}

// ToolCatalog lists every Book operation as an MCP tool, including init.
func ToolCatalog() []Tool {
	return intern.ToolCatalog()
}

// KnownTool reports whether name is in ToolCatalog.
func KnownTool(name string) bool {
	return intern.KnownTool(name)
}

// EncodeJSON renders a tool result the way the CLI and MCP server do.
func EncodeJSON(v any) ([]byte, error) {
	return intern.EncodeJSON(v)
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
