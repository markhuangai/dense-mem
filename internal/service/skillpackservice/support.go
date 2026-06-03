package skillpackservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
)

type supportBuilder struct {
	claims    map[string]SkillPackSupportClaim
	fragments map[string]SkillPackSupportFragment
}

type itemSupportInput struct {
	Claim       *SkillPackSupportClaim
	FragmentIDs []string
}

type supportImportState struct {
	claimsByID       map[string]SkillPackSupportClaim
	fragmentsByID    map[string]SkillPackSupportFragment
	localFragmentIDs map[string]string
}

func includeSupport(req ExportRequest) bool {
	return req.IncludeSupport == nil || *req.IncludeSupport
}

func newSupportBuilder() *supportBuilder {
	return &supportBuilder{
		claims:    map[string]SkillPackSupportClaim{},
		fragments: map[string]SkillPackSupportFragment{},
	}
}

func (b *supportBuilder) addClaim(claim *domain.Claim) []string {
	if claim == nil || claim.ClaimID == "" {
		return nil
	}
	b.claims[claim.ClaimID] = supportClaimFromDomain(claim)
	return supportFragmentIDsForClaim(claim)
}

func (b *supportBuilder) addFragments(fragments map[string]SkillPackSupportFragment) {
	for id, fragment := range fragments {
		if id != "" {
			b.fragments[id] = fragment
		}
	}
}

func (b *supportBuilder) build() *SkillPackSupport {
	if b == nil || (len(b.claims) == 0 && len(b.fragments) == 0) {
		return nil
	}
	support := &SkillPackSupport{
		Claims:    make([]SkillPackSupportClaim, 0, len(b.claims)),
		Fragments: make([]SkillPackSupportFragment, 0, len(b.fragments)),
	}
	for _, claim := range b.claims {
		support.Claims = append(support.Claims, claim)
	}
	for _, fragment := range b.fragments {
		support.Fragments = append(support.Fragments, fragment)
	}
	return support
}

func supportClaimFromDomain(claim *domain.Claim) SkillPackSupportClaim {
	return SkillPackSupportClaim{
		ClaimID:     claim.ClaimID,
		Subject:     claim.Subject,
		Predicate:   claim.Predicate,
		Object:      claim.Object,
		SupportedBy: supportFragmentIDsForClaim(claim),
	}
}

func supportFragmentIDsForClaim(claim *domain.Claim) []string {
	if claim == nil {
		return nil
	}
	ids := append([]string(nil), claim.SupportedBy...)
	for _, evidence := range claim.Evidence {
		if evidence.FragmentID != "" {
			ids = append(ids, evidence.FragmentID)
		}
	}
	return uniqueStrings(ids)
}

func supportFragmentIDsForFact(fact *domain.Fact) []string {
	if fact == nil {
		return nil
	}
	ids := []string{}
	for _, evidence := range fact.Evidence {
		if evidence.FragmentID != "" {
			ids = append(ids, evidence.FragmentID)
		}
	}
	return uniqueStrings(ids)
}

func (s *service) addFactSupport(ctx context.Context, profileID string, item *SkillPackItem, fact *domain.Fact, builder *supportBuilder) error {
	item.SourceID = fact.FactID
	fragmentIDs := supportFragmentIDsForFact(fact)
	if fact.PromotedFromClaimID != "" {
		if s.deps.ClaimGet == nil {
			return fmt.Errorf("skill pack export: claim get service is required to export support for fact %s", fact.FactID)
		}
		claim, err := s.deps.ClaimGet.Get(ctx, profileID, fact.PromotedFromClaimID)
		if err != nil {
			return err
		}
		if claim == nil {
			return fmt.Errorf("skill pack export: promoted claim %s not found for fact %s", fact.PromotedFromClaimID, fact.FactID)
		}
		item.SupportClaimIDs = append(item.SupportClaimIDs, claim.ClaimID)
		fragmentIDs = append(fragmentIDs, builder.addClaim(claim)...)
	}
	return s.addSupportFragments(ctx, profileID, item, builder, fragmentIDs)
}

func (s *service) addClaimSupport(ctx context.Context, profileID string, item *SkillPackItem, claim *domain.Claim, builder *supportBuilder) error {
	item.SourceID = claim.ClaimID
	item.SupportClaimIDs = append(item.SupportClaimIDs, claim.ClaimID)
	fragmentIDs := builder.addClaim(claim)
	return s.addSupportFragments(ctx, profileID, item, builder, fragmentIDs)
}

func (s *service) addSupportFragments(ctx context.Context, profileID string, item *SkillPackItem, builder *supportBuilder, fragmentIDs []string) error {
	fragmentIDs = uniqueStrings(fragmentIDs)
	if len(fragmentIDs) == 0 {
		return nil
	}
	fragments, err := s.graphOps.loadSupportFragments(ctx, profileID, fragmentIDs)
	if err != nil {
		return err
	}
	if missing := missingSupportFragments(fragmentIDs, fragments); len(missing) > 0 {
		return fmt.Errorf("skill pack export: support fragments not found or retracted: %s", strings.Join(missing, ", "))
	}
	builder.addFragments(fragments)
	item.SupportFragmentIDs = uniqueStrings(append(item.SupportFragmentIDs, fragmentIDs...))
	return nil
}

func missingSupportFragments(ids []string, fragments map[string]SkillPackSupportFragment) []string {
	missing := []string{}
	for _, id := range uniqueStrings(ids) {
		if _, exists := fragments[id]; !exists {
			missing = append(missing, id)
		}
	}
	return missing
}

func newSupportImportState(pack SkillPack) *supportImportState {
	state := &supportImportState{
		claimsByID:       map[string]SkillPackSupportClaim{},
		fragmentsByID:    map[string]SkillPackSupportFragment{},
		localFragmentIDs: map[string]string{},
	}
	if pack.Support == nil {
		return state
	}
	for _, claim := range pack.Support.Claims {
		state.claimsByID[claim.ClaimID] = claim
	}
	for _, fragment := range pack.Support.Fragments {
		state.fragmentsByID[fragment.FragmentID] = fragment
	}
	return state
}

func (s *service) importSupportForItem(ctx context.Context, profileID, importID, artifactHash, sourceURL, mode string, pack SkillPack, state *supportImportState, item SkillPackItem) (itemSupportInput, error) {
	if state == nil || (len(item.SupportClaimIDs) == 0 && len(item.SupportFragmentIDs) == 0) {
		return itemSupportInput{}, nil
	}
	support := itemSupportInput{}
	for _, claimID := range item.SupportClaimIDs {
		claim, exists := state.claimsByID[claimID]
		if !exists {
			return support, fmt.Errorf("support claim %s not found", claimID)
		}
		if support.Claim == nil {
			firstClaim := claim
			support.Claim = &firstClaim
		}
		support.FragmentIDs = append(support.FragmentIDs, claim.SupportedBy...)
	}
	support.FragmentIDs = append(support.FragmentIDs, item.SupportFragmentIDs...)
	originalFragmentIDs := uniqueStrings(support.FragmentIDs)
	localFragmentIDs := make([]string, 0, len(originalFragmentIDs))
	for _, originalID := range originalFragmentIDs {
		localID, err := s.importSupportFragment(ctx, profileID, importID, artifactHash, sourceURL, mode, pack, state, originalID)
		if err != nil {
			return support, err
		}
		localFragmentIDs = append(localFragmentIDs, localID)
	}
	support.FragmentIDs = uniqueStrings(localFragmentIDs)
	return support, nil
}

func (s *service) importSupportFragment(ctx context.Context, profileID, importID, artifactHash, sourceURL, mode string, pack SkillPack, state *supportImportState, originalID string) (string, error) {
	if localID := state.localFragmentIDs[originalID]; localID != "" {
		return localID, nil
	}
	fragment, exists := state.fragmentsByID[originalID]
	if !exists {
		return "", fmt.Errorf("support fragment %s not found", originalID)
	}
	sourceType := fragment.SourceType
	if sourceType == "" {
		sourceType = string(domain.SourceTypeManual)
	}
	authority := fragment.Authority
	if authority == "" {
		authority = importAuthority(mode)
	}
	source := fragment.Source
	if source == "" {
		source = importSource(pack, sourceURL)
	}
	metadata := map[string]any{}
	metadata["imported"] = true
	metadata["import_id"] = importID
	metadata["skill_pack_hash"] = artifactHash
	metadata["skill_pack_schema"] = pack.SchemaVersion
	metadata["source_fragment_id"] = originalID
	fragmentRes, err := s.deps.FragmentCreate.Create(ctx, profileID, &dto.CreateFragmentRequest{
		Content:        fragment.Content,
		SourceType:     sourceType,
		Source:         source,
		Authority:      authority,
		IdempotencyKey: skillPackImportKey("fragment", artifactHash, originalID),
		Labels:         appendImportLabel(fragment.Labels),
		Metadata:       metadata,
		SourceQuality:  importSourceQuality(mode, fragment.SourceQuality),
	})
	if err != nil {
		return "", err
	}
	localID := fragmentRes.Fragment.FragmentID
	state.localFragmentIDs[originalID] = localID
	if fragmentRes.Duplicate {
		return localID, nil
	}
	if err := s.graphOps.tagFragment(ctx, profileID, localID, importID, artifactHash); err != nil {
		return "", s.cleanupCreatedEntity(ctx, profileID, "fragment", localID, err)
	}
	if err := s.appendChange(ctx, profileID, importID, "fragment", localID, domain.SkillPackChangeActionCreated, nil, map[string]any{
		"fragment_id":  localID,
		"content_hash": fragmentRes.Fragment.ContentHash,
		"import_id":    importID,
	}); err != nil {
		return "", s.cleanupCreatedEntity(ctx, profileID, "fragment", localID, err)
	}
	return localID, nil
}

func importSourceQuality(mode string, exported *float64) float64 {
	if exported != nil {
		return *exported
	}
	return sourceQuality(mode)
}

func claimFromItem(item SkillPackItem, mode, artifactHash string, idx int, importID string, support itemSupportInput, fallbackFragmentID string) *domain.Claim {
	supportedBy := append([]string(nil), support.FragmentIDs...)
	if len(supportedBy) == 0 && fallbackFragmentID != "" {
		supportedBy = []string{fallbackFragmentID}
	}
	claim := &domain.Claim{
		Subject:           item.Subject,
		Predicate:         item.Predicate,
		Object:            item.Object,
		Modality:          domain.ModalityAssertion,
		Polarity:          domain.PolarityPlus,
		Speaker:           "skill_pack",
		ExtractConf:       confidenceFor(mode, item.SourceKind),
		ResolutionConf:    confidenceFor(mode, item.SourceKind),
		IdempotencyKey:    fmt.Sprintf("skill-pack:%s:%d", artifactHash, idx),
		SupportedBy:       supportedBy,
		ExtractionModel:   "skill_pack_import",
		ExtractionVersion: SchemaVersion,
		PipelineRunID:     importID,
	}
	if support.Claim == nil {
		return claim
	}
	source := support.Claim
	claim.IdempotencyKey = skillPackImportKey("claim", artifactHash, source.ClaimID)
	return claim
}

func skillPackImportKey(kind, artifactHash, sourceID string) string {
	sum := sha256.Sum256([]byte(kind + ":" + artifactHash + ":" + sourceID))
	return "skill-pack:" + kind + ":" + hex.EncodeToString(sum[:])
}

func skillPackFilename(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "skill-pack"
	}
	return slug + ".skill-pack.json"
}

func appendImportLabel(labels []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if _, exists := seen[label]; exists {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	if _, exists := seen["skill_pack_import"]; !exists && len(out) < 20 {
		out = append(out, "skill_pack_import")
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
