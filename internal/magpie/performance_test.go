package magpie

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func BenchmarkLoadState(b *testing.B) {
	b.Run("1000Notes", func(b *testing.B) {
		store, ctx := newBenchmarkMagpieStore(b)
		appendBenchmarkNotes(b, store, ctx, 1000)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			state, err := store.LoadState()
			if err != nil {
				b.Fatal(err)
			}
			if len(state.Notes) != 1000 {
				b.Fatalf("expected 1000 notes, got %d", len(state.Notes))
			}
		}
	})

	b.Run("100Accounts1000Journals", func(b *testing.B) {
		store, ctx := newBenchmarkMagpieStore(b)
		appendBenchmarkChartAndJournals(b, store, ctx, 100, 1000)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			state, err := store.LoadState()
			if err != nil {
				b.Fatal(err)
			}
			if len(state.Accounts) != 100 || len(state.JournalEntries) != 1000 {
				b.Fatalf("unexpected replayed state: accounts=%d journals=%d", len(state.Accounts), len(state.JournalEntries))
			}
		}
	})
}

func BenchmarkCreateManualJournalEntryGrowingLog(b *testing.B) {
	store, ctx := newBenchmarkMagpieStore(b)
	cash, revenue := appendBenchmarkChart(b, store, ctx, 1, 1)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := store.CreateJournalEntry(ctx, JournalEntry{
			Date:         "2026-07-16",
			Memo:         fmt.Sprintf("Benchmark manual journal %d", i),
			ManualReason: "benchmark manual journal write path",
			Postings: []Posting{
				{AccountID: cash[0], Debit: 1000},
				{AccountID: revenue[0], Credit: 1000},
			},
			Source:    "benchmark",
			SourceKey: fmt.Sprintf("manual-journal:%d", i),
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func newBenchmarkMagpieStore(b *testing.B) (*Store, Context) {
	b.Helper()
	store, err := OpenStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Errorf("close store: %v", err)
		}
	})
	store.now = func() time.Time {
		return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	}
	ctx := Context{Actor: "owner"}
	if _, err := store.WriteInitialRoot(ctx); err != nil {
		b.Fatal(err)
	}
	return store, ctx
}

func appendBenchmarkNotes(b *testing.B, store *Store, ctx Context, count int) {
	b.Helper()
	body := strings.Repeat("agent note body ", 32)
	now := store.now().UTC()
	for i := 0; i < count; i++ {
		note := Note{
			ID:          fmt.Sprintf("note:bench-%06d", i),
			Title:       fmt.Sprintf("Benchmark note %06d", i),
			Body:        body,
			Sensitivity: "internal",
			CreatedAt:   now,
			UpdatedAt:   now,
			CreatedBy:   ctx.Actor,
			UpdatedBy:   ctx.Actor,
		}
		if _, err := store.appendEvent(ctx, "note", note.ID, "note upsert", wrapEvent("note.upsert", noteUpsertPayload{Note: note}), true); err != nil {
			b.Fatal(err)
		}
	}
}

func appendBenchmarkChartAndJournals(b *testing.B, store *Store, ctx Context, accountCount int, journalCount int) {
	b.Helper()
	assetIDs, revenueIDs := appendBenchmarkChart(b, store, ctx, accountCount/2, accountCount-accountCount/2)
	now := store.now().UTC()
	for i := 0; i < journalCount; i++ {
		entry := JournalEntry{
			ID:              fmt.Sprintf("jrnl:bench-%06d", i),
			Date:            "2026-07-16",
			Memo:            fmt.Sprintf("Benchmark replay journal %06d", i),
			AccountingBasis: AccountingBasisCash,
			Origin:          JournalOriginMigration,
			Postings: []Posting{
				{AccountID: assetIDs[i%len(assetIDs)], Debit: 1000},
				{AccountID: revenueIDs[i%len(revenueIDs)], Credit: 1000},
			},
			Source:    "benchmark",
			SourceKey: fmt.Sprintf("replay-journal:%06d", i),
			CreatedAt: now,
			CreatedBy: ctx.Actor,
		}
		if _, err := store.appendEvent(ctx, "ledger.journal", entry.ID, "ledger journal create", wrapEvent("journal.create", journalCreatePayload{
			Entry:     entry,
			SourceKey: entry.Source + ":" + entry.SourceKey,
		}), true); err != nil {
			b.Fatal(err)
		}
	}
}

func appendBenchmarkChart(b *testing.B, store *Store, ctx Context, assetCount int, revenueCount int) ([]string, []string) {
	b.Helper()
	now := store.now().UTC()
	assetIDs := make([]string, 0, assetCount)
	revenueIDs := make([]string, 0, revenueCount)
	for i := 0; i < assetCount; i++ {
		account := Account{
			ID:          fmt.Sprintf("acct:bench-asset-%06d", i),
			Number:      fmt.Sprintf("1%03d", i),
			Name:        fmt.Sprintf("Benchmark Asset %06d", i),
			Type:        AccountAsset,
			Sensitivity: "internal",
			CreatedAt:   now,
			CreatedBy:   ctx.Actor,
		}
		if _, err := store.appendEvent(ctx, "ledger.account", account.ID, "ledger account create", wrapEvent("account.create", accountCreatePayload{Account: account}), true); err != nil {
			b.Fatal(err)
		}
		assetIDs = append(assetIDs, account.ID)
	}
	for i := 0; i < revenueCount; i++ {
		account := Account{
			ID:          fmt.Sprintf("acct:bench-revenue-%06d", i),
			Number:      fmt.Sprintf("4%03d", i),
			Name:        fmt.Sprintf("Benchmark Revenue %06d", i),
			Type:        AccountRevenue,
			Sensitivity: "internal",
			CreatedAt:   now,
			CreatedBy:   ctx.Actor,
		}
		if _, err := store.appendEvent(ctx, "ledger.account", account.ID, "ledger account create", wrapEvent("account.create", accountCreatePayload{Account: account}), true); err != nil {
			b.Fatal(err)
		}
		revenueIDs = append(revenueIDs, account.ID)
	}
	return assetIDs, revenueIDs
}
