package magpie

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWriterCatalogFitsJayBaseGrammarAndOneNamespace(t *testing.T) {
	entries := WriterCatalog()
	if err := validateWriterCatalog(entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("writer catalog is empty")
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Type, EventNamespace+".") {
			t.Fatalf("type %q is outside %s", entry.Type, EventNamespace)
		}
		if _, ok := legacyTypeAliases[entry.Type]; ok {
			t.Fatalf("catalog type %q collides with a legacy alias key", entry.Type)
		}
		seen[entry.Type] = struct{}{}
		for _, command := range entry.Commands {
			if strings.Contains(command, "%") || strings.ContainsAny(command, ":/\\") {
				t.Fatalf("command %q is not a stable exact string", command)
			}
		}
	}
	aliases := LegacyTypeAliases()
	for _, legacy := range []string{"customer", "invoice", "note", "payout"} {
		canonical, ok := aliases[legacy]
		if !ok {
			t.Fatalf("legacy undotted type %q is missing from the alias map", legacy)
		}
		if strings.Contains(legacy, ".") || !strings.HasPrefix(canonical, EventNamespace+".") {
			t.Fatalf("legacy type %q aliases %q", legacy, canonical)
		}
		if _, installed := seen[canonical]; !installed {
			t.Fatalf("legacy type %q aliases %q, which is not in the writer catalog", legacy, canonical)
		}
	}
	for legacy, canonical := range aliases {
		if _, ok := seen[canonical]; !ok {
			t.Fatalf("legacy type %q aliases unknown catalog type %q", legacy, canonical)
		}
		if _, ok := seen[legacy]; ok {
			t.Fatalf("legacy type %q is still in the writer catalog", legacy)
		}
	}
	if RefPattern != "magpie-*" || LegacyPeriodCloseRefPattern != "period-close-*" {
		t.Fatalf("ref patterns = %q and %q", RefPattern, LegacyPeriodCloseRefPattern)
	}
}

func TestCatalogAppendRejectsLegacyTypesAndDynamicCommands(t *testing.T) {
	if err := validateCatalogAppend(TypeNote, CmdNoteUpsert); err != nil {
		t.Fatal(err)
	}
	if err := validateCatalogAppend("note", CmdNoteUpsert); err == nil {
		t.Fatal("expected legacy undotted type to be rejected for new appends")
	}
	dynamic := fmt.Sprintf("note upsert %s", "note:1")
	if err := validateCatalogAppend(TypeNote, dynamic); err == nil {
		t.Fatal("expected dynamic command to be rejected")
	}
	if err := validateCatalogAppend(TypeNote, "note upsert extra"); err == nil {
		t.Fatal("expected unlisted command to be rejected")
	}
}

func TestLegacyUndottedTypesReplayAndNewWritesUseCatalogNames(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := Context{Actor: "owner"}

	root, err := s.appendEventAt(ctx, "store.init", "", "store init", initEvent(), "")
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Users["owner"].Role != "Owner" || state.Root != root {
		t.Fatalf("legacy store.init did not initialize: root=%s users=%#v", state.Root, state.Users)
	}
	repeated, err := s.WriteInitialRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != root {
		t.Fatalf("legacy init was appended again: got %s want %s", repeated, root)
	}

	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	customer := Customer{ID: "cust:legacy", Name: "Legacy Co", CreatedAt: now, UpdatedAt: now, CreatedBy: "owner", UpdatedBy: "owner"}
	root, err = s.appendEventAt(ctx, "customer", customer.ID, "customer upsert", wrapEvent("customer.upsert", customerUpsertPayload{Customer: customer}), root)
	if err != nil {
		t.Fatal(err)
	}
	invoice := Invoice{
		ID: "inv:legacy", InvoiceNumber: "L-1", CustomerID: customer.ID, InvoiceDate: "2026-07-01",
		Status: SourceDocumentOpen, CreatedAt: now, UpdatedAt: now, CreatedBy: "owner", UpdatedBy: "owner",
	}
	root, err = s.appendEventAt(ctx, "invoice", invoice.ID, "invoice create", wrapEvent("invoice.create", invoiceCreatePayload{Invoice: invoice}), root)
	if err != nil {
		t.Fatal(err)
	}
	note := Note{ID: "note:legacy", Title: "Legacy", Body: "kept", Sensitivity: "internal", CreatedAt: now, UpdatedAt: now, CreatedBy: "owner", UpdatedBy: "owner"}
	root, err = s.appendEventAt(ctx, "note", note.ID, "note upsert", wrapEvent("note.upsert", noteUpsertPayload{Note: note}), root)
	if err != nil {
		t.Fatal(err)
	}
	payout := Payout{ID: "payout:legacy", Date: "2026-07-01", Description: "legacy", NetAmountCents: 100, CreatedAt: now, UpdatedAt: now, CreatedBy: "owner", UpdatedBy: "owner"}
	root, err = s.appendEventAt(ctx, "payout", payout.ID, "payout create", wrapEvent("payout.create", payoutCreatePayload{Payout: payout}), root)
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultBookSettings()
	settings.AccountingBasis = AccountingBasisAccrual
	root, err = s.appendEventAt(ctx, "book.settings", "book:settings", "book settings set", wrapEvent("settings.update", settingsUpdatePayload{Settings: settings}), root)
	if err != nil {
		t.Fatal(err)
	}

	state, err = s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Customers[customer.ID].Name != "Legacy Co" || state.Invoices[invoice.ID].InvoiceNumber != "L-1" ||
		state.Notes[note.ID].Body != "kept" || state.Payouts[payout.ID].NetAmountCents != 100 ||
		state.Settings.AccountingBasis != AccountingBasisAccrual {
		t.Fatalf("legacy events did not replay: customers=%#v invoices=%#v notes=%#v payouts=%#v basis=%s",
			state.Customers, state.Invoices, state.Notes, state.Payouts, state.Settings.AccountingBasis)
	}

	created, _, err := s.UpsertCustomer(ctx, Customer{Name: "New Co"})
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, err := s.UpsertNote(ctx, "", "Fresh", "new type", "internal")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetAccountingBasis(ctx, AccountingBasisCash); err != nil {
		t.Fatal(err)
	}
	state, err = s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Customers[customer.ID].Name != "Legacy Co" || state.Customers[created.ID].Name != "New Co" ||
		state.Notes[note.ID].Body != "kept" || state.Notes[fresh.ID].Body != "new type" ||
		state.Settings.AccountingBasis != AccountingBasisCash {
		t.Fatalf("mixed legacy and catalog replay lost state: %#v", state)
	}

	nodes, err := s.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	wantLegacy := map[string]bool{"store.init": false, "customer": false, "invoice": false, "note": false, "payout": false, "book.settings": false}
	wantNew := map[string]bool{TypeCustomer: false, TypeNote: false, TypeBookSettings: false}
	for _, node := range nodes {
		if _, ok := wantLegacy[node.Type]; ok {
			wantLegacy[node.Type] = true
		}
		if _, ok := wantNew[node.Type]; ok {
			wantNew[node.Type] = true
		}
	}
	for typ, saw := range wantLegacy {
		if !saw {
			t.Fatalf("history dropped legacy type %q: %#v", typ, nodes)
		}
	}
	for typ, saw := range wantNew {
		if !saw {
			t.Fatalf("new write missing catalog type %q: %#v", typ, nodes)
		}
	}
}

func TestHistoricalPeriodCloseRefIsNotRenamed(t *testing.T) {
	s, _ := newTestStore(t)
	state, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	name := "period-close-2026-01-31-r1"
	if err := s.ensureCloseNamedRef(name, state.Root); err != nil {
		t.Fatal(err)
	}
	got, err := s.db.NamedRef(name)
	if err != nil || got != state.Root {
		t.Fatalf("legacy ref %q = %q err=%v", name, got, err)
	}
	if _, err := s.db.NamedRef(RefPrefix + name); err == nil {
		t.Fatal("historical period-close ref was copied under the magpie- prefix")
	}
}

func TestSnapshotRefsUseMagpiePrefix(t *testing.T) {
	s, owner := newTestStore(t)
	plain, err := s.CreateSnapshot(owner, "pre-close")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Name != "magpie-pre-close" {
		t.Fatalf("snapshot name = %q", plain.Name)
	}
	prefixed, err := s.CreateSnapshot(owner, "magpie-already")
	if err != nil {
		t.Fatal(err)
	}
	if prefixed.Name != "magpie-already" {
		t.Fatalf("prefixed snapshot name = %q", prefixed.Name)
	}
	if _, err := s.CreateSnapshot(owner, "../escape"); err == nil {
		t.Fatal("expected unsafe snapshot name to be rejected")
	}
}
