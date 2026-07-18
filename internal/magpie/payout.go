package magpie

import (
	"strings"
	"time"
)

type payoutCreatePayload struct {
	Payout Payout `json:"payout"`
}

type payoutUpdatePayload struct {
	Payout Payout `json:"payout"`
}

func (s *Store) GetPayout(ctx Context, payoutID string) (Payout, error) {
	st, err := s.LoadState()
	if err != nil {
		return Payout{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerRead); err != nil {
		return Payout{}, err
	}
	payoutID = strings.TrimSpace(payoutID)
	if payoutID == "" {
		return Payout{}, appErr(ErrValidation, "payout id is required")
	}
	payout, ok := st.Payouts[payoutID]
	if !ok {
		return Payout{}, appErr(ErrNotFound, "payout %s not found", payoutID)
	}
	return payout, nil
}

func (s *Store) ImportPayout(ctx Context, payout Payout) (Payout, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Payout{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Payout{}, "", err
	}
	payout, err = normalizePayout(st, payout)
	if err != nil {
		return Payout{}, "", err
	}
	root := st.Root
	if existingID, ok := payoutIDByExternalRefs(st, payout.ExternalRefs, payout.ID); ok {
		payout = st.Payouts[existingID]
	} else if existing, ok := st.Payouts[payout.ID]; ok {
		if !externalRefsEqual(existing.ExternalRefs, payout.ExternalRefs) ||
			existing.Date != payout.Date ||
			existing.SourceAccountID != payout.SourceAccountID ||
			existing.DestinationAccountID != payout.DestinationAccountID ||
			existing.NetAmountCents != payout.NetAmountCents ||
			existing.FeeAmountCents != payout.FeeAmountCents ||
			existing.FeeExpenseAccountID != payout.FeeExpenseAccountID {
			return Payout{}, "", appErr(ErrConflict, "payout %s already exists with different details", payout.ID)
		}
		payout = existing
	} else {
		now := s.now().UTC()
		payout.CreatedAt = now
		payout.UpdatedAt = now
		payout.CreatedBy = ctx.Actor
		payout.UpdatedBy = ctx.Actor
		hash, err := s.appendEventAt(ctx, "payout", payout.ID, "payout create", wrapEvent("payout.create", payoutCreatePayload{Payout: payout}), root)
		if err != nil {
			return Payout{}, "", err
		}
		root = hash
	}

	expectedJournals := 1
	if payout.FeeAmountCents > 0 {
		expectedJournals = 2
	}
	if len(payout.JournalEntryIDs) == expectedJournals {
		return payout, root, nil
	}

	receive, newRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               payout.Date,
		Memo:               payoutMemo("Payout received", payout),
		Workflow:           "payout.receive",
		PostingSemantics:   "payout_received",
		SourceDocumentType: "payout",
		SourceDocumentID:   payout.ID,
		Source:             "payout",
		SourceKey:          payout.ID + ":received",
		Postings: []Posting{
			{AccountID: payout.DestinationAccountID, Debit: payout.NetAmountCents, Memo: "Payout received"},
			{AccountID: payout.SourceAccountID, Credit: payout.NetAmountCents, Memo: "Clear payout source"},
		},
		Metadata: payoutJournalMetadata(payout),
	})
	if err != nil {
		return Payout{}, "", err
	}
	if newRoot != "" {
		root = newRoot
	}
	journalIDs := []string{receive.ID}

	if payout.FeeAmountCents > 0 {
		fee, newRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
			Date:               payout.Date,
			Memo:               payoutMemo("Payout fees", payout),
			Workflow:           "payout.fee",
			PostingSemantics:   "payout_fee",
			SourceDocumentType: "payout",
			SourceDocumentID:   payout.ID,
			Source:             "payout",
			SourceKey:          payout.ID + ":fee",
			Postings: []Posting{
				{AccountID: payout.FeeExpenseAccountID, Debit: payout.FeeAmountCents, Memo: "Merchant processing fees"},
				{AccountID: payout.SourceAccountID, Credit: payout.FeeAmountCents, Memo: "Clear payout source fees"},
			},
			Metadata: payoutJournalMetadata(payout),
		})
		if err != nil {
			return Payout{}, "", err
		}
		if newRoot != "" {
			root = newRoot
		}
		journalIDs = append(journalIDs, fee.ID)
	}

	payout.JournalEntryIDs = journalIDs
	payout.UpdatedAt = s.now().UTC()
	payout.UpdatedBy = ctx.Actor
	hash, err := s.appendEventAt(ctx, "payout", payout.ID, "payout journal links update", wrapEvent("payout.update", payoutUpdatePayload{Payout: payout}), root)
	if err != nil {
		return Payout{}, "", err
	}
	if hash != "" {
		root = hash
	}
	return payout, root, nil
}

func normalizePayout(st State, payout Payout) (Payout, error) {
	payout.ID = strings.TrimSpace(payout.ID)
	payout.Date = strings.TrimSpace(payout.Date)
	payout.Description = strings.TrimSpace(payout.Description)
	payout.SourceAccountID = strings.TrimSpace(payout.SourceAccountID)
	payout.DestinationAccountID = strings.TrimSpace(payout.DestinationAccountID)
	payout.FeeExpenseAccountID = strings.TrimSpace(payout.FeeExpenseAccountID)
	if payout.Date == "" {
		return Payout{}, appErr(ErrValidation, "payout date is required")
	}
	if _, err := time.Parse("2006-01-02", payout.Date); err != nil {
		return Payout{}, appErr(ErrValidation, "payout date must use YYYY-MM-DD")
	}
	if payout.NetAmountCents <= 0 {
		return Payout{}, appErr(ErrValidation, "payout net amount must be positive")
	}
	if payout.FeeAmountCents < 0 {
		return Payout{}, appErr(ErrValidation, "payout fee amount cannot be negative")
	}
	source, ok := st.Accounts[payout.SourceAccountID]
	if !ok {
		return Payout{}, appErr(ErrValidation, "payout source account %s not found", payout.SourceAccountID)
	}
	if source.Type != AccountAsset {
		return Payout{}, appErr(ErrValidation, "payout source account must be an asset account")
	}
	destination, ok := st.Accounts[payout.DestinationAccountID]
	if !ok {
		return Payout{}, appErr(ErrValidation, "payout destination account %s not found", payout.DestinationAccountID)
	}
	if destination.Role != AccountRoleOperatingCash && destination.Role != AccountRoleBankAccount {
		return Payout{}, appErr(ErrValidation, "payout destination account must have role %q or %q", AccountRoleOperatingCash, AccountRoleBankAccount)
	}
	if payout.SourceAccountID == payout.DestinationAccountID {
		return Payout{}, appErr(ErrValidation, "payout source and destination accounts must differ")
	}
	if payout.FeeAmountCents > 0 {
		fee, ok := st.Accounts[payout.FeeExpenseAccountID]
		if !ok {
			return Payout{}, appErr(ErrValidation, "payout fee expense account %s not found", payout.FeeExpenseAccountID)
		}
		if fee.Role != AccountRoleMerchantFeesExpense {
			return Payout{}, appErr(ErrValidation, "payout fee expense account must have role %q", AccountRoleMerchantFeesExpense)
		}
	} else {
		payout.FeeExpenseAccountID = ""
	}
	refs, err := normalizeExternalRefs(payout.ExternalRefs)
	if err != nil {
		return Payout{}, err
	}
	if len(refs) == 0 {
		return Payout{}, appErr(ErrValidation, "payout external_refs are required for idempotent import")
	}
	payout.ExternalRefs = refs
	if payout.ID == "" {
		payout.ID = makeID("payout", externalRefKey(refs[0]))
	}
	return payout, nil
}

func payoutIDByExternalRefs(st State, refs []ExternalSourceRef, currentPayoutID string) (string, bool) {
	for _, ref := range refs {
		key := externalRefKey(ref)
		for _, existing := range st.Payouts {
			if existing.ID == currentPayoutID {
				continue
			}
			for _, existingRef := range existing.ExternalRefs {
				if externalRefKey(existingRef) == key {
					return existing.ID, true
				}
			}
		}
	}
	return "", false
}

func payoutMemo(prefix string, payout Payout) string {
	if payout.Description != "" {
		return prefix + " " + payout.Description
	}
	if len(payout.ExternalRefs) > 0 {
		return prefix + " " + payout.ExternalRefs[0].ExternalID
	}
	return prefix + " " + payout.ID
}

func payoutJournalMetadata(payout Payout) map[string]string {
	if len(payout.ExternalRefs) == 0 {
		return nil
	}
	ref := payout.ExternalRefs[0]
	metadata := map[string]string{
		"external_source_system": ref.SourceSystem,
		"external_id":            ref.ExternalID,
	}
	if ref.ExternalType != "" {
		metadata["external_type"] = ref.ExternalType
	}
	return metadata
}
