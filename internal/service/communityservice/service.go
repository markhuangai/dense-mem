package communityservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/community"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	runLease                        = 15 * time.Minute
	inputLimit                      = community.MaxNodes + 1
	maxSummaryRunAttempts           = 3
	maxSummaryRelationships         = 100
	maxSummaryEvidenceIDs           = 200
	maxSummaryQuotes                = 200
	maxSummaryQuotesPerRelationship = 3
	maxSummaryQuoteRunes            = 1000
	communitySummaryPromptVersion   = "community-summary-v1"
)

type service struct {
	store   repository.CommunityRepository
	config  AppConfig
	summary SummaryProvider
	metrics interface{}
	now     func() time.Time
}

func New(deps Dependencies) Service {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{store: deps.Store, config: deps.AppConfig, summary: deps.Summary, now: now, metrics: deps.Metrics}
}

func (s *service) RunScheduled(ctx context.Context, teamID string, windowAt time.Time) (*RunResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("community: repository is required")
	}
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("community: invalid team id: %w", err)
	}
	if windowAt.IsZero() {
		windowAt = s.now()
	}
	windowKey := windowAt.UTC().Format("2006-01-02")
	inputs, err := s.store.ListCommunityInputs(ctx, repository.CommunityInputListInput{TeamID: teamID, Limit: inputLimit})
	if err != nil {
		return nil, fmt.Errorf("community: list graph inputs: %w", err)
	}
	configurationHash := community.ConfigurationHash(community.DefaultSeed)
	providerModel := ""
	if s.summary != nil {
		providerModel = s.summary.ModelName()
	}
	sourceFingerprint, err := fingerprintInputs(inputs, configurationHash, providerModel)
	if err != nil {
		return nil, err
	}
	if latest, latestErr := s.store.LatestCommunityRun(ctx, teamID); latestErr == nil && latest != nil &&
		latest.WindowKey == windowKey && latest.Status == "completed" && latest.SourceFingerprint == sourceFingerprint {
		return runResultFromRecord(latest, "skipped"), nil
	}
	lineage, err := s.store.ListCurrentCommunityLineage(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("community: list current lineage: %w", err)
	}
	run, err := s.store.ClaimCommunityRun(ctx, repository.CommunityRunClaimInput{
		TeamID:            teamID,
		WindowKey:         windowKey,
		LeaseUntil:        s.now().Add(runLease),
		AlgorithmKind:     community.AlgorithmKind,
		AlgorithmVersion:  community.AlgorithmVersion,
		ProfileVersion:    repository.CommunityProfileVersion,
		ConfigurationHash: configurationHash,
		SourceFingerprint: sourceFingerprint,
		MaxNodes:          community.MaxNodes,
		MaxEdges:          community.MaxEdges,
	})
	if err != nil {
		return nil, err
	}
	if !run.Claimed {
		return runResultFromRecord(run, "skipped"), nil
	}
	result := &RunResult{
		RunID: run.RunID, TeamID: teamID, WindowKey: windowKey,
		Status: "running", SourceFingerprint: sourceFingerprint, StartedAt: run.StartedAt,
	}
	leaseCtx, stopLease := context.WithCancel(ctx)
	leaseDone := make(chan struct{})
	leaseErr := make(chan error, 1)
	go func() {
		defer close(leaseDone)
		ticker := time.NewTicker(runLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				if renewErr := s.store.RenewCommunityRunLease(leaseCtx, repository.CommunityRunLeaseInput{
					TeamID: teamID, RunID: run.RunID, LeaseUntil: s.now().Add(runLease),
				}); renewErr != nil {
					select {
					case leaseErr <- renewErr:
					default:
					}
					return
				}
			}
		}
	}()
	defer func() {
		stopLease()
		<-leaseDone
	}()
	leaseFailure := func() error {
		select {
		case err := <-leaseErr:
			return err
		default:
			return nil
		}
	}
	finish := func(status, message string) (*RunResult, error) {
		result.Status = status
		result.Error = boundedError(message)
		result.CompletedAt = s.now()
		completeErr := s.store.CompleteCommunityRun(ctx, repository.CommunityRunCompleteInput{
			TeamID: teamID, RunID: run.RunID, Status: status,
			NodeCount: result.NodeCount, EdgeCount: result.EdgeCount,
			CommunityCount: result.CommunityCount, Error: result.Error,
		})
		if completeErr != nil {
			return nil, completeErr
		}
		if metrics, ok := s.metrics.(observability.DiscoverabilityMetrics); ok {
			observability.RecordCommunityRun(ctx, metrics, status, result.NodeCount, result.EdgeCount, result.CommunityCount)
		}
		return result, nil
	}
	graphResult := community.Detect(toCommunityInputs(inputs), community.DefaultSeed)
	result.NodeCount = len(graphResult.Nodes)
	result.EdgeCount = len(graphResult.Edges)
	if graphResult.TooLarge {
		return finish("too_large", "community graph exceeds the fixed node or edge bound")
	}
	logicalIDs := matchLogicalIDs(graphResult.Clusters, lineage)
	previousByLogicalID := make(map[string]repository.CommunityLineageRecord, len(lineage))
	for _, record := range lineage {
		previousByLogicalID[record.LogicalCommunityID] = record
	}
	communities, providerModel, attempts, buildErr := s.buildPublishRecords(ctx, teamID, run.RunID, inputs, graphResult, logicalIDs, previousByLogicalID, sourceFingerprint, providerModel)
	result.ProviderModel = providerModel
	result.ProviderAttempts = attempts
	result.CommunityCount = len(communities)
	if buildErr != nil {
		return finish("failed", "community summary generation failed")
	}
	if renewErr := leaseFailure(); renewErr != nil {
		return finish("failed", "community run lease renewal failed")
	}
	latestInputs, latestErr := s.store.ListCommunityInputs(ctx, repository.CommunityInputListInput{TeamID: teamID, Limit: inputLimit})
	if latestErr != nil {
		return finish("failed", "community source refresh failed")
	}
	latestFingerprint, fingerprintErr := fingerprintInputs(latestInputs, configurationHash, providerModel)
	if fingerprintErr != nil || latestFingerprint != sourceFingerprint {
		return finish("failed", "community source changed during summary generation")
	}
	if renewErr := leaseFailure(); renewErr != nil {
		return finish("failed", "community run lease renewal failed")
	}
	if err := s.store.PublishCommunitySnapshot(ctx, repository.CommunitySnapshotPublishInput{
		TeamID: teamID, RunID: run.RunID,
		AlgorithmKind: community.AlgorithmKind, AlgorithmVersion: community.AlgorithmVersion,
		ProfileVersion: repository.CommunityProfileVersion, ConfigurationHash: configurationHash,
		SourceFingerprint: sourceFingerprint, SourceSnapshot: sourceSnapshot(inputs),
		NodeCount: result.NodeCount, EdgeCount: result.EdgeCount, Communities: communities,
	}); err != nil {
		return finish("failed", "community snapshot publication failed")
	}
	result.Status = "completed"
	result.CompletedAt = s.now()
	if metrics, ok := s.metrics.(observability.DiscoverabilityMetrics); ok {
		observability.RecordCommunityRun(ctx, metrics, "completed", result.NodeCount, result.EdgeCount, result.CommunityCount)
	}
	return result, nil
}

func (s *service) Status(ctx context.Context, teamID string) (*StatusResult, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("community: repository is required")
	}
	status := &StatusResult{}
	if s.config != nil {
		cfg, err := s.config.CommunityDetectionRuntimeConfig(ctx)
		if err != nil {
			return nil, err
		}
		status.EffectiveConfig = cfg
	}
	if run, err := s.store.LatestCommunityRun(ctx, teamID); err != nil {
		return nil, err
	} else if run != nil {
		status.LatestRun = runResultFromRecord(run, run.Status)
	}
	if count, err := s.store.CountCurrentCommunities(ctx, teamID); err != nil {
		return nil, err
	} else {
		status.CurrentCommunityCount = count
	}
	return status, nil
}

func (s *service) buildPublishRecords(ctx context.Context, teamID, runID string, inputs []repository.CommunityInput, detected community.Result, logicalIDs map[string]string, previousByLogicalID map[string]repository.CommunityLineageRecord, sourceFingerprint, configuredProviderModel string) ([]repository.CommunityPublishRecord, string, int, error) {
	byGroup := make(map[string][]repository.CommunityInput)
	for _, input := range inputs {
		byGroup[input.SemanticGroupKey] = append(byGroup[input.SemanticGroupKey], input)
	}
	records := make([]repository.CommunityPublishRecord, 0, len(detected.Clusters))
	providerModel := configuredProviderModel
	attempts := 0
	for ordinal, cluster := range detected.Clusters {
		clusterInputs := make([]repository.CommunityInput, 0)
		for _, group := range cluster.GroupKeys {
			clusterInputs = append(clusterInputs, byGroup[group]...)
		}
		sort.Slice(clusterInputs, func(i, j int) bool { return clusterInputs[i].RelationshipID < clusterInputs[j].RelationshipID })
		if len(clusterInputs) == 0 {
			continue
		}
		record := repository.CommunityPublishRecord{
			CommunityID: uuid.NewString(), LogicalCommunityID: logicalIDs[strings.Join(cluster.GroupKeys, "\x00")],
			Ordinal: ordinal, MemberCount: len(uniqueEntityEndpoints(clusterInputs)), SourceCount: len(clusterInputs),
			SummaryVersion: "community-louvain-v2", SourceFingerprint: sourceFingerprint,
		}
		if record.LogicalCommunityID == "" {
			record.LogicalCommunityID = cluster.CommunityID.String()
		}
		record.TopEntities, record.Memberships = topEntitiesAndMemberships(clusterInputs)
		record.TopPredicates = topPredicates(clusterInputs)
		record.Sources = make([]repository.CommunitySourceInput, 0, len(clusterInputs))
		for sourceRank, input := range clusterInputs {
			sourceHash := sourceStateHash(input)
			record.Sources = append(record.Sources, repository.CommunitySourceInput{
				RelationshipID: input.RelationshipID, OwnerProfileID: input.OwnerProfileID,
				RelationshipVersion: input.Version, SourceRank: sourceRank,
				SemanticGroupKey: input.SemanticGroupKey, SourceStateHash: sourceHash,
			})
		}
		summaryRelationships := boundedSummaryRelationships(clusterInputs)
		record.SummaryInputHash = hashJSON(summaryRelationships)
		if previous, ok := previousByLogicalID[record.LogicalCommunityID]; ok &&
			previous.SummaryInputHash == record.SummaryInputHash &&
			strings.TrimSpace(previous.SummaryProviderModel) == strings.TrimSpace(configuredProviderModel) &&
			previous.SummaryPromptHash == summaryPromptHash(record.SummaryInputHash) &&
			strings.TrimSpace(previous.Summary) != "" {
			record.Summary = previous.Summary
			record.SummaryVersion = firstNonEmpty(previous.SummaryVersion, record.SummaryVersion)
			record.SummaryProviderModel = previous.SummaryProviderModel
			record.SummaryPromptHash = previous.SummaryPromptHash
			record.SummaryResponseHash = previous.SummaryResponseHash
			records = append(records, record)
			continue
		}
		var provider string
		var usedAttempts int
		var summaryResponse domain.CommunitySummary
		var summaryErr error
		summaryResponse, provider, usedAttempts, summaryErr = s.summarize(ctx, teamID, runID, record.CommunityID, record.LogicalCommunityID, record.SummaryInputHash, summaryRelationships, record.TopEntities, record.TopPredicates)
		record.Summary = summaryResponse.Summary
		providerModel = firstNonEmpty(providerModel, provider)
		attempts += usedAttempts
		if summaryErr != nil {
			if metrics, ok := s.metrics.(observability.DiscoverabilityMetrics); ok {
				observability.RecordCommunitySummary(ctx, metrics, "failed", usedAttempts)
			}
			return nil, providerModel, attempts, summaryErr
		}
		if metrics, ok := s.metrics.(observability.DiscoverabilityMetrics); ok {
			observability.RecordCommunitySummary(ctx, metrics, "ok", usedAttempts)
		}
		record.SummaryProviderModel = provider
		record.SummaryPromptHash = summaryPromptHash(record.SummaryInputHash)
		record.SummaryResponseHash = firstNonEmpty(summaryResponse.ResponseHash, hashString(record.Summary))
		record.SummaryVersion = firstNonEmpty(record.SummaryVersion, "community-louvain-v2")
		record.SourceFingerprint = sourceFingerprint
		records = append(records, record)
	}
	_ = teamID
	_ = runID
	return records, providerModel, attempts, nil
}

func (s *service) summarize(ctx context.Context, teamID, runID, versionCommunityID, logicalCommunityID, inputHash string, relationships []domain.CommunitySummaryRelationship, topEntities, topPredicates []string) (domain.CommunitySummary, string, int, error) {
	if s.summary == nil {
		return domain.CommunitySummary{}, "", 0, errors.New("community summary provider is unavailable")
	}
	providerModel := s.summary.ModelName()
	promptHash := summaryPromptHash(inputHash)
	for attempt := 1; attempt <= maxSummaryRunAttempts; attempt++ {
		response, err := s.summary.SummarizeCommunity(ctx, domain.CommunitySummaryInput{CommunityID: logicalCommunityID, SummaryInputHash: inputHash, Relationships: relationships})
		if err != nil {
			if recordErr := s.store.RecordCommunitySummaryAttempt(ctx, repository.CommunitySummaryAttemptInput{
				TeamID: teamID, RunID: runID, CommunityID: versionCommunityID, Attempt: attempt,
				ProviderModel: providerModel, PromptHash: promptHash, InputHash: inputHash, ErrorCode: "provider_error",
			}); recordErr != nil {
				return domain.CommunitySummary{}, providerModel, attempt, fmt.Errorf("community summary attempt persistence failed: %w", recordErr)
			}
			if attempt == maxSummaryRunAttempts {
				return domain.CommunitySummary{}, providerModel, attempt, fmt.Errorf("community summary provider failed after %d attempts", attempt)
			}
			continue
		}
		if validationError := validateCommunitySummaryResponse(response, inputHash, relationships); validationError != "" {
			if recordErr := s.store.RecordCommunitySummaryAttempt(ctx, repository.CommunitySummaryAttemptInput{
				TeamID: teamID, RunID: runID, CommunityID: versionCommunityID, Attempt: attempt,
				ProviderModel: firstNonEmpty(response.ProviderModel, providerModel), PromptHash: promptHash,
				ResponseHash: response.ResponseHash, InputHash: inputHash,
				AdmittedRelationshipIDs: append([]string(nil), response.AdmittedRelationshipIDs...),
				AdmittedEvidenceIDs:     append([]string(nil), response.AdmittedEvidenceIDs...),
				AdmittedSupportQuotes:   append([]domain.CommunitySummarySupportQuote(nil), response.AdmittedSupportQuotes...),
				ErrorCode:               validationError,
			}); recordErr != nil {
				return domain.CommunitySummary{}, providerModel, attempt, fmt.Errorf("community summary attempt persistence failed: %w", recordErr)
			}
			if attempt == maxSummaryRunAttempts {
				return domain.CommunitySummary{}, providerModel, attempt, fmt.Errorf("community summary provider returned an invalid complete response: %s", validationError)
			}
			continue
		}
		response.Summary = strings.TrimSpace(response.Summary)
		if recordErr := s.store.RecordCommunitySummaryAttempt(ctx, repository.CommunitySummaryAttemptInput{
			TeamID: teamID, RunID: runID, CommunityID: versionCommunityID, Attempt: attempt,
			ProviderModel: firstNonEmpty(response.ProviderModel, providerModel), PromptHash: promptHash,
			ResponseHash: response.ResponseHash, InputHash: inputHash,
			AdmittedRelationshipIDs: append([]string(nil), response.AdmittedRelationshipIDs...),
			AdmittedEvidenceIDs:     append([]string(nil), response.AdmittedEvidenceIDs...),
			AdmittedSupportQuotes:   append([]domain.CommunitySummarySupportQuote(nil), response.AdmittedSupportQuotes...),
			ResponseSummary:         response.Summary, Valid: true,
		}); recordErr != nil {
			return domain.CommunitySummary{}, providerModel, attempt, fmt.Errorf("community summary attempt persistence failed: %w", recordErr)
		}
		return response, firstNonEmpty(response.ProviderModel, providerModel), attempt, nil
	}
	return domain.CommunitySummary{}, providerModel, maxSummaryRunAttempts, errors.New("community summary unavailable")
}

func boundedSummaryRelationships(inputs []repository.CommunityInput) []domain.CommunitySummaryRelationship {
	out := make([]domain.CommunitySummaryRelationship, 0, summaryMinInt(len(inputs), maxSummaryRelationships))
	seenEvidence := map[string]struct{}{}
	seenQuotes := map[string]struct{}{}
	quoteCount := 0
	evidenceCount := 0
	for _, input := range inputs {
		if len(out) == maxSummaryRelationships {
			break
		}
		relationship := domain.CommunitySummaryRelationship{
			RelationshipID: input.RelationshipID,
			Subject:        input.SubjectName,
			Predicate:      input.PredicateKey,
			Object:         firstNonEmpty(input.ObjectName, input.ObjectValue),
		}
		for _, evidenceID := range input.EvidenceIDs {
			evidenceID = strings.TrimSpace(evidenceID)
			if evidenceID == "" || evidenceCount == maxSummaryEvidenceIDs {
				continue
			}
			if _, ok := seenEvidence[evidenceID]; ok {
				continue
			}
			seenEvidence[evidenceID] = struct{}{}
			relationship.EvidenceIDs = append(relationship.EvidenceIDs, evidenceID)
			evidenceCount++
		}
		for _, quote := range input.EvidenceQuotes {
			if quoteCount == maxSummaryQuotes || len(relationship.SupportQuotes) == maxSummaryQuotesPerRelationship {
				break
			}
			evidenceID := strings.TrimSpace(quote.EvidenceID)
			text := strings.TrimSpace(quote.Quote)
			if evidenceID == "" || text == "" {
				continue
			}
			if _, ok := seenQuotes[evidenceID]; ok {
				continue
			}
			if _, ok := seenEvidence[evidenceID]; !ok {
				if evidenceCount == maxSummaryEvidenceIDs {
					continue
				}
				seenEvidence[evidenceID] = struct{}{}
				relationship.EvidenceIDs = append(relationship.EvidenceIDs, evidenceID)
				evidenceCount++
			}
			relationship.SupportQuotes = append(relationship.SupportQuotes, domain.CommunitySummarySupportQuote{
				EvidenceID: evidenceID,
				Quote:      truncateSummaryQuote(text),
			})
			seenQuotes[evidenceID] = struct{}{}
			quoteCount++
		}
		out = append(out, relationship)
	}
	return out
}

func truncateSummaryQuote(value string) string {
	runes := []rune(value)
	if len(runes) <= maxSummaryQuoteRunes {
		return value
	}
	return string(runes[:maxSummaryQuoteRunes])
}

func summaryMinInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func validateCommunitySummaryResponse(response domain.CommunitySummary, inputHash string, relationships []domain.CommunitySummaryRelationship) string {
	switch {
	case strings.TrimSpace(response.Summary) == "":
		return "summary_empty"
	case utf8.RuneCountInString(response.Summary) > 4000:
		return "summary_too_long"
	case response.InputHash != "" && response.InputHash != inputHash:
		return "input_hash_mismatch"
	case !subsetStrings(response.AdmittedRelationshipIDs, relationshipIDs(relationships)):
		return "admitted_relationship_ids_not_allowlisted"
	case !subsetStrings(response.AdmittedEvidenceIDs, evidenceIDs(relationships)):
		return "admitted_evidence_ids_not_allowlisted"
	case !uniqueStrings(response.AdmittedRelationshipIDs):
		return "admitted_relationship_ids_not_unique"
	case !uniqueStrings(response.AdmittedEvidenceIDs):
		return "admitted_evidence_ids_not_unique"
	case !validSupportQuotes(response.AdmittedSupportQuotes, supportQuotes(relationships)):
		return "admitted_support_quotes_not_exact"
	case !validTopEntities(response.TopEntities, relationships):
		return "top_entities_not_allowlisted"
	case !validTopPredicates(response.TopPredicates, relationships):
		return "top_predicates_not_allowlisted"
	default:
		return ""
	}
}

func toCommunityInputs(inputs []repository.CommunityInput) []community.Input {
	out := make([]community.Input, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, community.Input{RelationshipID: input.RelationshipID, SemanticGroupKey: input.SemanticGroupKey,
			SubjectEntityID: input.SubjectEntityID, ObjectEntityID: input.ObjectEntityID, ObjectValueID: input.ObjectValueID,
			EvidenceIDs: input.EvidenceIDs, PredicateKey: input.PredicateKey, SubjectName: input.SubjectName, ObjectName: input.ObjectName})
	}
	return out
}

func fingerprintInputs(inputs []repository.CommunityInput, configurationHash, providerModel string) (string, error) {
	copyInputs := append([]repository.CommunityInput(nil), inputs...)
	sort.Slice(copyInputs, func(i, j int) bool {
		if copyInputs[i].SemanticGroupKey != copyInputs[j].SemanticGroupKey {
			return copyInputs[i].SemanticGroupKey < copyInputs[j].SemanticGroupKey
		}
		return copyInputs[i].RelationshipID < copyInputs[j].RelationshipID
	})
	return hashJSON(struct {
		ConfigurationHash string                      `json:"configuration_hash"`
		ProviderModel     string                      `json:"provider_model"`
		Inputs            []repository.CommunityInput `json:"inputs"`
	}{configurationHash, strings.TrimSpace(providerModel), copyInputs}), nil
}

func sourceSnapshot(inputs []repository.CommunityInput) []map[string]any {
	out := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, map[string]any{"relationship_id": input.RelationshipID, "version": input.Version, "semantic_group_key": input.SemanticGroupKey, "source_state_hash": sourceStateHash(input)})
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["relationship_id"]) < fmt.Sprint(out[j]["relationship_id"])
	})
	return out
}

func sourceStateHash(input repository.CommunityInput) string {
	return hashJSON([]any{input.RelationshipID, input.Version, input.SemanticGroupKey, input.EvidenceIDs, input.ObjectEntityID, input.ObjectValueID})
}
func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func summaryPromptHash(inputHash string) string {
	return hashString(inputHash + ":prompt:" + communitySummaryPromptVersion)
}

func hashJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return hashString(string(encoded))
}

func runResultFromRecord(record *repository.CommunityRun, status string) *RunResult {
	if record == nil {
		return nil
	}
	return &RunResult{RunID: record.RunID, TeamID: record.TeamID, WindowKey: record.WindowKey, Status: status, NodeCount: record.NodeCount, EdgeCount: record.EdgeCount, CommunityCount: record.CommunityCount, SourceFingerprint: record.SourceFingerprint, Error: publicCommunityRunError(status), StartedAt: record.StartedAt, CompletedAt: derefTime(record.CompletedAt)}
}

func publicCommunityRunError(status string) string {
	switch status {
	case "failed":
		return "community run failed"
	case "too_large":
		return "community graph exceeded the configured bound"
	case "cancelled":
		return "community run was cancelled"
	default:
		return ""
	}
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}
func boundedError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1000 {
		return value[:1000]
	}
	return value
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func subsetStrings(values, allowed []string) bool {
	if len(values) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		set[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}
func relationshipIDs(values []domain.CommunitySummaryRelationship) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.RelationshipID)
	}
	return out
}
func evidenceIDs(values []domain.CommunitySummaryRelationship) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		for _, id := range value.EvidenceIDs {
			set[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func supportQuotes(values []domain.CommunitySummaryRelationship) []domain.CommunitySummarySupportQuote {
	out := make([]domain.CommunitySummarySupportQuote, 0)
	seen := map[string]string{}
	for _, value := range values {
		for _, quote := range value.SupportQuotes {
			id := strings.TrimSpace(quote.EvidenceID)
			text := strings.TrimSpace(quote.Quote)
			if id == "" || text == "" {
				continue
			}
			if existing, ok := seen[id]; ok && existing != text {
				continue
			}
			seen[id] = text
		}
	}
	for id, quote := range seen {
		out = append(out, domain.CommunitySummarySupportQuote{EvidenceID: id, Quote: quote})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EvidenceID < out[j].EvidenceID })
	return out
}

func validSupportQuotes(values, allowed []domain.CommunitySummarySupportQuote) bool {
	allowedByID := make(map[string]string, len(allowed))
	for _, quote := range allowed {
		allowedByID[quote.EvidenceID] = quote.Quote
	}
	seen := map[string]struct{}{}
	for _, quote := range values {
		if _, duplicate := seen[quote.EvidenceID]; duplicate {
			return false
		}
		seen[quote.EvidenceID] = struct{}{}
		if allowedByID[quote.EvidenceID] != quote.Quote {
			return false
		}
	}
	return true
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validTopEntities(values []string, relationships []domain.CommunitySummaryRelationship) bool {
	allowed := map[string]struct{}{}
	for _, relationship := range relationships {
		for _, value := range []string{relationship.Subject, relationship.Object} {
			if value = strings.TrimSpace(value); value != "" {
				allowed[value] = struct{}{}
			}
		}
	}
	return len(values) <= 5 && uniqueStrings(values) && subsetStrings(values, mapKeys(allowed))
}

func validTopPredicates(values []string, relationships []domain.CommunitySummaryRelationship) bool {
	allowed := map[string]struct{}{}
	for _, relationship := range relationships {
		if value := strings.TrimSpace(relationship.Predicate); value != "" {
			allowed[value] = struct{}{}
		}
	}
	return len(values) <= 5 && uniqueStrings(values) && subsetStrings(values, mapKeys(allowed))
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func uniqueEntityEndpoints(inputs []repository.CommunityInput) []string {
	set := map[string]struct{}{}
	for _, input := range inputs {
		for _, id := range []string{input.SubjectEntityID, input.ObjectEntityID} {
			if id != "" {
				set[id] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func topEntitiesAndMemberships(inputs []repository.CommunityInput) ([]string, []repository.CommunityMembershipInput) {
	type count struct {
		id, name string
		n        int
	}
	counts := map[string]*count{}
	for _, input := range inputs {
		for _, entity := range []struct{ id, name string }{{input.SubjectEntityID, input.SubjectName}, {input.ObjectEntityID, input.ObjectName}} {
			if entity.id == "" {
				continue
			}
			item := counts[entity.id]
			if item == nil {
				item = &count{id: entity.id, name: entity.name}
				counts[entity.id] = item
			}
			item.n++
		}
	}
	items := make([]count, 0, len(counts))
	for _, item := range counts {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].n != items[j].n {
			return items[i].n > items[j].n
		}
		if items[i].name != items[j].name {
			return items[i].name < items[j].name
		}
		return items[i].id < items[j].id
	})
	memberships := make([]repository.CommunityMembershipInput, 0, len(items))
	names := make([]string, 0, 5)
	for rank, item := range items {
		memberships = append(memberships, repository.CommunityMembershipInput{EntityID: item.id, Rank: rank, MembershipScore: 1, SourceCount: item.n})
		if len(names) < 5 {
			names = append(names, firstNonEmpty(item.name, item.id))
		}
	}
	return names, memberships
}
func topPredicates(inputs []repository.CommunityInput) []string {
	counts := map[string]int{}
	for _, input := range inputs {
		counts[input.PredicateKey]++
	}
	values := make([]string, 0, len(counts))
	for key := range counts {
		values = append(values, key)
	}
	sort.Slice(values, func(i, j int) bool {
		if counts[values[i]] != counts[values[j]] {
			return counts[values[i]] > counts[values[j]]
		}
		return values[i] < values[j]
	})
	if len(values) > 5 {
		values = values[:5]
	}
	return values
}
func matchLogicalIDs(clusters []community.Cluster, previous []repository.CommunityLineageRecord) map[string]string {
	used := map[string]bool{}
	out := map[string]string{}
	for _, cluster := range clusters {
		best := repository.CommunityLineageRecord{}
		bestScore := 0.0
		for _, candidate := range previous {
			if used[candidate.LogicalCommunityID] {
				continue
			}
			score := jaccard(cluster.GroupKeys, candidate.GroupKeys)
			if score < 0.60 {
				continue
			}
			if score > bestScore || (score == bestScore && candidate.LogicalCommunityID < best.LogicalCommunityID) {
				best, bestScore = candidate, score
			}
		}
		key := strings.Join(cluster.GroupKeys, "\x00")
		if best.LogicalCommunityID != "" {
			out[key] = best.LogicalCommunityID
			used[best.LogicalCommunityID] = true
		} else {
			out[key] = cluster.CommunityID.String()
		}
	}
	return out
}
func jaccard(left, right []string) float64 {
	a := map[string]struct{}{}
	b := map[string]struct{}{}
	for _, v := range left {
		a[v] = struct{}{}
	}
	for _, v := range right {
		b[v] = struct{}{}
	}
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for v := range a {
		if _, ok := b[v]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
