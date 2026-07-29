package magpie

import (
	"fmt"
	"strings"
	"time"
)

type fixedAssetCreatePayload struct {
	Asset FixedAsset `json:"asset"`
}

type fixedAssetUpdatePayload struct {
	Asset FixedAsset `json:"asset"`
}

func (s *Store) GetFixedAsset(ctx Context, assetID string) (FixedAsset, error) {
	st, err := s.LoadState()
	if err != nil {
		return FixedAsset{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerRead); err != nil {
		return FixedAsset{}, err
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return FixedAsset{}, appErr(ErrValidation, "fixed asset id is required")
	}
	asset, ok := st.FixedAssets[assetID]
	if !ok {
		return FixedAsset{}, appErr(ErrNotFound, "fixed asset %s not found", assetID)
	}
	return asset, nil
}

// AcquireFixedAsset creates a fixed-asset register record and the canonical
// acquisition journal. It intentionally supports book depreciation only.
func (s *Store) AcquireFixedAsset(ctx Context, asset FixedAsset) (FixedAsset, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return FixedAsset{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return FixedAsset{}, "", err
	}
	asset, err = normalizeFixedAsset(st, asset, s.now().UTC())
	if err != nil {
		return FixedAsset{}, "", err
	}
	if existingID, ok := fixedAssetIDByExternalRefs(st, asset.ExternalRefs, asset.ID); ok {
		asset.ID = existingID
	}

	root := st.Root
	existing, exists := st.FixedAssets[asset.ID]
	if exists {
		if !fixedAssetEquivalent(existing, asset) {
			return FixedAsset{}, "", appErr(ErrConflict, "fixed asset %s already exists with different details", asset.ID)
		}
		asset = existing
	} else {
		now := s.now().UTC()
		asset.CreatedAt = now
		asset.UpdatedAt = now
		asset.CreatedBy = ctx.Actor
		asset.UpdatedBy = ctx.Actor
		hash, err := s.appendEventAt(ctx, "fixed_asset", asset.ID, "fixed asset acquire", wrapEvent("fixed_asset.create", fixedAssetCreatePayload{Asset: asset}), root)
		if err != nil {
			return FixedAsset{}, "", err
		}
		root = hash
	}

	acquisition, journalRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               asset.AcquisitionDate,
		Memo:               "Acquire fixed asset: " + asset.Name,
		Workflow:           "fixed_asset.acquire",
		PostingSemantics:   "fixed_asset_capitalized",
		SourceDocumentType: "fixed_asset",
		SourceDocumentID:   asset.ID,
		Source:             "fixed_asset",
		SourceKey:          asset.ID + ":acquisition",
		Postings: []Posting{
			{AccountID: asset.FixedAssetAccountID, Debit: asset.CostCents, Memo: "Capitalize fixed asset"},
			{AccountID: asset.FundingAccountID, Credit: asset.CostCents, Memo: "Fund fixed asset acquisition"},
		},
		Metadata: map[string]string{
			"depreciation_method":     string(asset.DepreciationMethod),
			"depreciation_convention": string(asset.DepreciationConvention),
			"useful_life_months":      fmt.Sprintf("%d", asset.UsefulLifeMonths),
		},
	})
	if err != nil {
		return FixedAsset{}, "", err
	}
	if journalRoot != "" {
		root = journalRoot
	}
	if asset.AcquisitionJournalEntryID == acquisition.ID {
		return asset, root, nil
	}
	asset.AcquisitionJournalEntryID = acquisition.ID
	asset.UpdatedAt = s.now().UTC()
	asset.UpdatedBy = ctx.Actor
	hash, err := s.appendEventAt(ctx, "fixed_asset", asset.ID, "fixed asset acquisition journal link", wrapEvent("fixed_asset.update", fixedAssetUpdatePayload{Asset: asset}), root)
	if err != nil {
		return FixedAsset{}, "", err
	}
	return asset, hash, nil
}

func (s *Store) DepreciateFixedAsset(ctx Context, assetID, throughDate string) (FixedAssetDepreciationResult, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return FixedAssetDepreciationResult{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return FixedAssetDepreciationResult{}, "", err
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return FixedAssetDepreciationResult{}, "", appErr(ErrValidation, "fixed asset id is required")
	}
	asset, ok := st.FixedAssets[assetID]
	if !ok {
		return FixedAssetDepreciationResult{}, "", appErr(ErrNotFound, "fixed asset %s not found", assetID)
	}
	if asset.AcquisitionJournalEntryID == "" {
		return FixedAssetDepreciationResult{}, "", appErr(ErrConflict, "fixed asset acquisition is incomplete; rerun fixed-asset acquire-json before depreciation")
	}
	acquisition, ok := st.JournalEntries[asset.AcquisitionJournalEntryID]
	if !ok || !isCanonicalAcquisitionJournal(acquisition, asset) {
		return FixedAssetDepreciationResult{}, "", appErr(ErrIntegrity, "fixed asset acquisition journal is missing or non-canonical")
	}
	asOf, err := parseRequiredDate(throughDate, "through date")
	if err != nil {
		return FixedAssetDepreciationResult{}, "", err
	}
	if asOf.After(s.now().UTC()) {
		return FixedAssetDepreciationResult{}, "", appErr(ErrValidation, "through date cannot be in the future")
	}

	schedule, err := buildFixedAssetSchedule(asset, asOf, st)
	if err != nil {
		return FixedAssetDepreciationResult{}, "", err
	}
	root := st.Root
	posted := make([]JournalEntry, 0)
	journalIDs := append([]string(nil), asset.DepreciationJournalEntryIDs...)
	for _, period := range schedule.Periods {
		if period.Status == "posted" {
			journalIDs = appendJournalID(journalIDs, period.JournalEntryID)
			continue
		}
		if period.Status != "due" {
			continue
		}
		entry, journalRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
			Date:               period.Date,
			Memo:               fmt.Sprintf("Depreciation: %s (%s)", asset.Name, period.Date[:7]),
			Workflow:           "fixed_asset.depreciate",
			PostingSemantics:   "straight_line_monthly_depreciation",
			SourceDocumentType: "fixed_asset",
			SourceDocumentID:   asset.ID,
			Source:             "fixed_asset",
			SourceKey:          fmt.Sprintf("%s:depreciation:%04d", asset.ID, period.Period),
			Postings: []Posting{
				{AccountID: asset.DepreciationExpenseAccountID, Debit: period.AmountCents, Memo: "Book depreciation expense"},
				{AccountID: asset.AccumulatedDepreciationAccountID, Credit: period.AmountCents, Memo: "Accumulated depreciation"},
			},
			Metadata: map[string]string{
				"asset_id":                asset.ID,
				"depreciation_period":     fmt.Sprintf("%d", period.Period),
				"depreciation_method":     string(asset.DepreciationMethod),
				"depreciation_convention": string(asset.DepreciationConvention),
				"book_depreciation_only":  "true",
			},
		})
		if err != nil {
			return FixedAssetDepreciationResult{}, "", err
		}
		if journalRoot != "" {
			root = journalRoot
		}
		posted = append(posted, entry)
		journalIDs = appendJournalID(journalIDs, entry.ID)
	}

	if !sameStrings(journalIDs, asset.DepreciationJournalEntryIDs) {
		asset.DepreciationJournalEntryIDs = journalIDs
		asset.UpdatedAt = s.now().UTC()
		asset.UpdatedBy = ctx.Actor
		hash, err := s.appendEventAt(ctx, "fixed_asset", asset.ID, "fixed asset depreciation journal links", wrapEvent("fixed_asset.update", fixedAssetUpdatePayload{Asset: asset}), root)
		if err != nil {
			return FixedAssetDepreciationResult{}, "", err
		}
		root = hash
	}

	finalState, err := s.LoadState()
	if err != nil {
		return FixedAssetDepreciationResult{}, "", err
	}
	finalSchedule, err := buildFixedAssetSchedule(asset, asOf, finalState)
	if err != nil {
		return FixedAssetDepreciationResult{}, "", err
	}
	return FixedAssetDepreciationResult{
		Asset:                asset,
		PostedJournalEntries: posted,
		AlreadyPostedPeriods: countScheduleStatus(finalSchedule.Periods, "posted") - len(posted),
		RemainingPeriods:     countScheduleStatus(finalSchedule.Periods, "scheduled"),
		AccumulatedCents:     finalSchedule.AccumulatedDepreciationCents,
		NetBookValueCents:    finalSchedule.NetBookValueCents,
	}, root, nil
}

func (s *Store) FixedAssetSchedule(ctx Context, assetID, asOfDate string) (FixedAssetSchedule, error) {
	st, err := s.LoadState()
	if err != nil {
		return FixedAssetSchedule{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerRead); err != nil {
		return FixedAssetSchedule{}, err
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return FixedAssetSchedule{}, appErr(ErrValidation, "fixed asset id is required")
	}
	asset, ok := st.FixedAssets[assetID]
	if !ok {
		return FixedAssetSchedule{}, appErr(ErrNotFound, "fixed asset %s not found", assetID)
	}
	var asOf time.Time
	if strings.TrimSpace(asOfDate) == "" {
		asOf = s.now().UTC()
	} else {
		asOf, err = parseRequiredDate(asOfDate, "as-of date")
		if err != nil {
			return FixedAssetSchedule{}, err
		}
		if asOf.After(s.now().UTC()) {
			return FixedAssetSchedule{}, appErr(ErrValidation, "as-of date cannot be in the future")
		}
	}
	return buildFixedAssetSchedule(asset, asOf, st)
}

func normalizeFixedAsset(st State, asset FixedAsset, now time.Time) (FixedAsset, error) {
	settings := st.effectiveSettings()
	if settings.AccountingBasis == AccountingBasisCash {
		return FixedAsset{}, appErr(ErrValidation, "fixed asset depreciation requires modified_cash or accrual accounting; cash-basis books expense purchases when paid")
	}
	if settings.AccountingBasis == AccountingBasisModifiedCash && !settings.ModifiedCashPolicy.CapitalizeFixedAssets {
		return FixedAsset{}, appErr(ErrValidation, "modified-cash policy does not permit fixed asset capitalization")
	}
	asset.ID = strings.TrimSpace(asset.ID)
	asset.Name = strings.TrimSpace(asset.Name)
	asset.Description = strings.TrimSpace(asset.Description)
	asset.AcquisitionDate = strings.TrimSpace(asset.AcquisitionDate)
	asset.PlacedInServiceDate = strings.TrimSpace(asset.PlacedInServiceDate)
	asset.FixedAssetAccountID = strings.TrimSpace(asset.FixedAssetAccountID)
	asset.AccumulatedDepreciationAccountID = strings.TrimSpace(asset.AccumulatedDepreciationAccountID)
	asset.DepreciationExpenseAccountID = strings.TrimSpace(asset.DepreciationExpenseAccountID)
	asset.FundingAccountID = strings.TrimSpace(asset.FundingAccountID)
	asset.DepreciationMethod = DepreciationMethod(strings.ToLower(strings.TrimSpace(string(asset.DepreciationMethod))))
	asset.DepreciationConvention = DepreciationConvention(strings.ToLower(strings.TrimSpace(string(asset.DepreciationConvention))))
	asset.AcquisitionJournalEntryID = ""
	asset.DepreciationJournalEntryIDs = nil
	asset.CreatedAt = time.Time{}
	asset.UpdatedAt = time.Time{}
	asset.CreatedBy = ""
	asset.UpdatedBy = ""
	if asset.Name == "" {
		return FixedAsset{}, appErr(ErrValidation, "fixed asset name is required")
	}
	acquisitionDate, err := parseRequiredDate(asset.AcquisitionDate, "acquisition date")
	if err != nil {
		return FixedAsset{}, err
	}
	placedDate, err := parseRequiredDate(asset.PlacedInServiceDate, "placed-in-service date")
	if err != nil {
		return FixedAsset{}, err
	}
	if placedDate.Before(acquisitionDate) {
		return FixedAsset{}, appErr(ErrValidation, "placed-in-service date cannot precede acquisition date")
	}
	if acquisitionDate.After(now) || placedDate.After(now) {
		return FixedAsset{}, appErr(ErrValidation, "acquisition and placed-in-service dates cannot be in the future")
	}
	if asset.CostCents <= 0 {
		return FixedAsset{}, appErr(ErrValidation, "fixed asset cost must be positive")
	}
	if asset.SalvageValueCents < 0 || asset.SalvageValueCents >= asset.CostCents {
		return FixedAsset{}, appErr(ErrValidation, "salvage value must be non-negative and less than cost")
	}
	if asset.UsefulLifeMonths < 1 || asset.UsefulLifeMonths > 1200 {
		return FixedAsset{}, appErr(ErrValidation, "useful life must be between 1 and 1200 months")
	}
	if asset.CostCents-asset.SalvageValueCents < int64(asset.UsefulLifeMonths) {
		return FixedAsset{}, appErr(ErrValidation, "depreciable basis must provide at least one cent per useful-life month; reduce useful life or expense the purchase")
	}
	if asset.DepreciationMethod == "" {
		asset.DepreciationMethod = DepreciationMethodStraightLine
	}
	if asset.DepreciationMethod != DepreciationMethodStraightLine {
		return FixedAsset{}, appErr(ErrValidation, "depreciation method must be %q", DepreciationMethodStraightLine)
	}
	if asset.DepreciationConvention == "" {
		asset.DepreciationConvention = DepreciationConventionFullMonth
	}
	if asset.DepreciationConvention != DepreciationConventionFullMonth {
		return FixedAsset{}, appErr(ErrValidation, "depreciation convention must be %q", DepreciationConventionFullMonth)
	}
	if err := requireAccountRole(st, asset.FixedAssetAccountID, AccountRoleFixedAsset, "fixed asset account"); err != nil {
		return FixedAsset{}, err
	}
	if err := requireAccountRole(st, asset.AccumulatedDepreciationAccountID, AccountRoleAccumulatedDepreciation, "accumulated depreciation account"); err != nil {
		return FixedAsset{}, err
	}
	if err := requireAccountRole(st, asset.DepreciationExpenseAccountID, AccountRoleDepreciationExpense, "depreciation expense account"); err != nil {
		return FixedAsset{}, err
	}
	funding, ok := st.Accounts[asset.FundingAccountID]
	if !ok {
		return FixedAsset{}, appErr(ErrValidation, "funding account %s not found", asset.FundingAccountID)
	}
	if funding.Type != AccountAsset && funding.Type != AccountLiability {
		return FixedAsset{}, appErr(ErrValidation, "funding account must be an asset or liability account")
	}
	accountIDs := []string{
		asset.FixedAssetAccountID,
		asset.AccumulatedDepreciationAccountID,
		asset.DepreciationExpenseAccountID,
		asset.FundingAccountID,
	}
	seenAccountIDs := map[string]bool{}
	for _, accountID := range accountIDs {
		if seenAccountIDs[accountID] {
			return FixedAsset{}, appErr(ErrValidation, "fixed asset workflow accounts must all differ")
		}
		seenAccountIDs[accountID] = true
	}
	asset.ExternalRefs, err = normalizeExternalRefs(asset.ExternalRefs)
	if err != nil {
		return FixedAsset{}, err
	}
	if asset.ID == "" {
		idBasis := strings.ToLower(asset.Name) + ":" + asset.AcquisitionDate + ":" + fmt.Sprintf("%d", asset.CostCents)
		if len(asset.ExternalRefs) > 0 {
			idBasis = externalRefKey(asset.ExternalRefs[0])
		}
		asset.ID = makeID("asset", idBasis)
	}
	return asset, nil
}

func buildFixedAssetSchedule(asset FixedAsset, asOf time.Time, st State) (FixedAssetSchedule, error) {
	placed, err := parseRequiredDate(asset.PlacedInServiceDate, "placed-in-service date")
	if err != nil {
		return FixedAssetSchedule{}, err
	}
	basis := asset.CostCents - asset.SalvageValueCents
	base := basis / int64(asset.UsefulLifeMonths)
	remainder := basis % int64(asset.UsefulLifeMonths)
	periods := make([]DepreciationSchedulePeriod, 0, asset.UsefulLifeMonths)
	accumulated := int64(0)
	for i := 0; i < asset.UsefulLifeMonths; i++ {
		amount := base
		if int64(i) < remainder {
			amount++
		}
		date := monthEnd(placed, i)
		journalID := ""
		sourceKey := "fixed_asset:" + fmt.Sprintf("%s:depreciation:%04d", asset.ID, i+1)
		if id, ok := st.SourceKeys[sourceKey]; ok {
			entry, exists := st.JournalEntries[id]
			if !exists {
				return FixedAssetSchedule{}, appErr(ErrIntegrity, "depreciation source key %q references missing journal %s", sourceKey, id)
			}
			if !isCanonicalDepreciationJournal(entry, asset, i+1, date.Format("2006-01-02"), amount) {
				return FixedAssetSchedule{}, appErr(ErrIntegrity, "depreciation source key %q references a non-canonical journal", sourceKey)
			}
			journalID = id
			accumulated += amount
		}
		status := "scheduled"
		if journalID != "" {
			status = "posted"
		} else if !date.After(asOf) {
			status = "due"
		}
		periods = append(periods, DepreciationSchedulePeriod{
			Period:         i + 1,
			Date:           date.Format("2006-01-02"),
			AmountCents:    amount,
			Status:         status,
			JournalEntryID: journalID,
		})
	}
	return FixedAssetSchedule{
		AssetID:                      asset.ID,
		AsOfDate:                     asOf.Format("2006-01-02"),
		CostCents:                    asset.CostCents,
		SalvageValueCents:            asset.SalvageValueCents,
		DepreciableBasisCents:        basis,
		AccumulatedDepreciationCents: accumulated,
		NetBookValueCents:            asset.CostCents - accumulated,
		Periods:                      periods,
	}, nil
}

func isCanonicalDepreciationJournal(entry JournalEntry, asset FixedAsset, period int, date string, amount int64) bool {
	if entry.Date != date ||
		entry.Origin != JournalOriginWorkflow ||
		entry.Workflow != "fixed_asset.depreciate" ||
		entry.PostingSemantics != "straight_line_monthly_depreciation" ||
		entry.SourceDocumentType != "fixed_asset" ||
		entry.SourceDocumentID != asset.ID ||
		entry.Source != "fixed_asset" ||
		entry.SourceKey != fmt.Sprintf("%s:depreciation:%04d", asset.ID, period) ||
		len(entry.Postings) != 2 {
		return false
	}
	return entry.Postings[0].AccountID == asset.DepreciationExpenseAccountID &&
		entry.Postings[0].Debit == amount &&
		entry.Postings[0].Credit == 0 &&
		entry.Postings[1].AccountID == asset.AccumulatedDepreciationAccountID &&
		entry.Postings[1].Debit == 0 &&
		entry.Postings[1].Credit == amount
}

func isCanonicalAcquisitionJournal(entry JournalEntry, asset FixedAsset) bool {
	if entry.Date != asset.AcquisitionDate ||
		entry.Origin != JournalOriginWorkflow ||
		entry.Workflow != "fixed_asset.acquire" ||
		entry.PostingSemantics != "fixed_asset_capitalized" ||
		entry.SourceDocumentType != "fixed_asset" ||
		entry.SourceDocumentID != asset.ID ||
		entry.Source != "fixed_asset" ||
		entry.SourceKey != asset.ID+":acquisition" ||
		len(entry.Postings) != 2 {
		return false
	}
	return entry.Postings[0].AccountID == asset.FixedAssetAccountID &&
		entry.Postings[0].Debit == asset.CostCents &&
		entry.Postings[0].Credit == 0 &&
		entry.Postings[1].AccountID == asset.FundingAccountID &&
		entry.Postings[1].Debit == 0 &&
		entry.Postings[1].Credit == asset.CostCents
}

func parseRequiredDate(value, label string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, appErr(ErrValidation, "%s is required", label)
	}
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, appErr(ErrValidation, "%s must use YYYY-MM-DD", label)
	}
	return date, nil
}

func monthEnd(placed time.Time, offset int) time.Time {
	first := time.Date(placed.Year(), placed.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first.AddDate(0, offset+1, -1)
}

func requireAccountRole(st State, accountID string, role AccountRole, label string) error {
	account, ok := st.Accounts[accountID]
	if !ok {
		return appErr(ErrValidation, "%s %s not found", label, accountID)
	}
	if account.Role != role {
		return appErr(ErrValidation, "%s must have role %q", label, role)
	}
	return nil
}

func fixedAssetIDByExternalRefs(st State, refs []ExternalSourceRef, currentID string) (string, bool) {
	for _, ref := range refs {
		key := externalRefKey(ref)
		for _, existing := range st.FixedAssets {
			if existing.ID == currentID {
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

func fixedAssetEquivalent(a, b FixedAsset) bool {
	return a.Name == b.Name &&
		a.Description == b.Description &&
		a.AcquisitionDate == b.AcquisitionDate &&
		a.PlacedInServiceDate == b.PlacedInServiceDate &&
		a.CostCents == b.CostCents &&
		a.SalvageValueCents == b.SalvageValueCents &&
		a.UsefulLifeMonths == b.UsefulLifeMonths &&
		a.DepreciationMethod == b.DepreciationMethod &&
		a.DepreciationConvention == b.DepreciationConvention &&
		a.FixedAssetAccountID == b.FixedAssetAccountID &&
		a.AccumulatedDepreciationAccountID == b.AccumulatedDepreciationAccountID &&
		a.DepreciationExpenseAccountID == b.DepreciationExpenseAccountID &&
		a.FundingAccountID == b.FundingAccountID &&
		externalRefsEqual(a.ExternalRefs, b.ExternalRefs)
}

func appendJournalID(ids []string, id string) []string {
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func countScheduleStatus(periods []DepreciationSchedulePeriod, status string) int {
	count := 0
	for _, period := range periods {
		if period.Status == status {
			count++
		}
	}
	return count
}
