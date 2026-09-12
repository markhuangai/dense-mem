package recall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const recallFeedbackEventPruneInterval = time.Hour
const DefaultRecallFeedbackRetentionDays = 30

// ErrRecallFeedbackInvalidInput identifies feedback that cannot be repaired by
// retrying the same server operation.
var (
	ErrRecallFeedbackInvalidInput     = errors.New("recall feedback input is invalid")
	ErrRecallFeedbackInvalidResultRef = errors.New("recall feedback result reference is invalid")
	ErrRecallFeedbackEventNotFound    = recallcontract.ErrRecallFeedbackEventNotFound
)

type RecallFeedbackEventReader interface {
	ListRecallFeedbackEvents(ctx context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error)
	GetRecallFeedbackEvent(ctx context.Context, recallID string) (*domain.RecallFeedbackEvent, error)
}

type RecallFeedbackEventRecorder interface {
	RecordRecallSnapshot(ctx context.Context, event domain.RecallFeedbackEvent) error
	RecordRecallFeedback(ctx context.Context, feedback domain.RecallFeedbackSubmission) error
}

type RecallFeedbackEventService interface {
	RecallFeedbackEventReader
	RecallFeedbackEventRecorder
	Prune(ctx context.Context) error
	Start(ctx context.Context)
	Shutdown(ctx context.Context) error
}

type RecallFeedbackEventStore = recallcontract.FeedbackEventRepository

type RecallFeedbackRetentionProvider interface {
	RecallFeedbackRuntimeConfig(ctx context.Context) (domain.RecallFeedbackRuntimeConfig, error)
}

type RecallFeedbackResultResolver interface {
	ResolveRecallFeedbackResults(ctx context.Context, profileID string, refs []domain.RecallFeedbackResultRef) ([]domain.RecallFeedbackResolvedResult, error)
}

// RecallFeedbackBatchResult is the native policy result for one ordered
// feedback submission batch. A failure stops the batch at FailedIndex while
// retaining the count of submissions committed before it.
type RecallFeedbackBatchResult struct {
	Recorded      bool
	RecordedCount int
	Failure       *RecallFeedbackBatchFailure
}

// RecallFeedbackBatchFailure carries the bounded public guidance for the
// first failed item in a feedback batch. Transports decide how to encode it.
type RecallFeedbackBatchFailure struct {
	PartialSuccess bool
	FailedIndex    int
	Error          string
	ErrorCode      string
	ReasonCode     string
	NextAction     string
	Remediation    string
}

// SubmitRecallFeedbackBatch applies feedback in request order and records a
// metric only after each persistence operation succeeds.
func SubmitRecallFeedbackBatch(
	ctx context.Context,
	recorder RecallFeedbackEventRecorder,
	metrics recallcontract.FeedbackMetrics,
	submissions []domain.RecallFeedbackSubmission,
) RecallFeedbackBatchResult {
	result := RecallFeedbackBatchResult{}
	for index, submission := range submissions {
		var err error
		if recorder == nil {
			err = errors.New("recall feedback recorder unavailable")
		} else {
			err = recorder.RecordRecallFeedback(ctx, submission)
		}
		if err != nil {
			guidance := recallFeedbackFailureGuidance(err)
			result.Failure = &RecallFeedbackBatchFailure{
				PartialSuccess: result.RecordedCount > 0,
				FailedIndex:    index,
				Error:          "recall feedback submission failed",
				ErrorCode:      guidance.errorCode,
				ReasonCode:     guidance.reasonCode,
				NextAction:     guidance.nextAction,
				Remediation:    guidance.remediation,
			}
			result.Recorded = result.RecordedCount > 0
			return result
		}
		recordRecallFeedbackMetric(ctx, metrics, recallcontract.FeedbackObservation{
			Used:            submission.Used,
			AnswerSupported: submission.AnswerSupported,
			Quality:         submission.Quality,
			MissingContext:  submission.MissingContext,
			Irrelevant:      submission.Irrelevant,
		})
		result.RecordedCount++
	}
	result.Recorded = result.RecordedCount > 0
	return result
}

func recordRecallFeedbackMetric(ctx context.Context, metrics recallcontract.FeedbackMetrics, observation recallcontract.FeedbackObservation) {
	if metrics == nil {
		return
	}
	if contextual, ok := metrics.(recallcontract.ContextualFeedbackMetrics); ok {
		contextual.ObserveRecallFeedbackFor(ctx, observation)
		return
	}
	metrics.ObserveRecallFeedback(observation)
}

type recallFeedbackFailureGuidanceResult struct {
	errorCode   string
	reasonCode  string
	nextAction  string
	remediation string
}

func recallFeedbackFailureGuidance(err error) recallFeedbackFailureGuidanceResult {
	guidance := recallFeedbackFailureGuidanceResult{
		errorCode:   "degraded",
		reasonCode:  "feedback_persistence_failed",
		nextAction:  "retry_same_request",
		remediation: "Retry the same feedback request with unchanged items after the service recovers.",
	}
	switch {
	case errors.Is(err, ErrRecallFeedbackInvalidResultRef):
		guidance.errorCode = "invalid_input"
		guidance.reasonCode = "result_reference_invalid"
		guidance.nextAction = "correct_and_resubmit"
		guidance.remediation = "Correct or remove the invalid irrelevant_result_refs or hypothesis_feedback references and resubmit with the current recall_event_id."
	case errors.Is(err, ErrRecallFeedbackEventNotFound):
		guidance.errorCode = "invalid_input"
		guidance.reasonCode = "reference_not_found"
		guidance.nextAction = "correct_and_resubmit"
		guidance.remediation = "Use a current recall_event_id and resubmit the corrected feedback items."
	case errors.Is(err, ErrRecallFeedbackInvalidInput):
		guidance.errorCode = "invalid_input"
		guidance.reasonCode = "invalid_feedback"
		guidance.nextAction = "correct_and_resubmit"
		guidance.remediation = "Correct the feedback fields and resubmit the request."
	case errors.Is(err, context.Canceled):
		guidance.errorCode = "degraded"
		guidance.reasonCode = "request_cancelled"
		guidance.nextAction = "stop"
		guidance.remediation = "Stop this feedback submission; retry only if the caller still needs it."
	case errors.Is(err, context.DeadlineExceeded):
		guidance.reasonCode = "request_timeout"
		guidance.remediation = "Retry the same feedback request with unchanged items after the timeout clears."
	}
	return guidance
}

// FeedbackResultRefs converts a Recall result into the stable, content-free
// references stored with its feedback snapshot. References retain first-seen
// ordering while duplicate ranks collapse to the earliest positive rank.
func FeedbackResultRefs(res *RecallResult) []domain.RecallFeedbackResultRef {
	if res == nil {
		return []domain.RecallFeedbackResultRef{}
	}
	refs := make([]domain.RecallFeedbackResultRef, 0, len(res.Results)*2+len(res.RelatedRelationships)+len(res.RelatedCommunities))
	seen := map[string]int{}
	appendRef := func(ref domain.RecallFeedbackResultRef) {
		key := ref.Type + "\x00" + ref.ID
		if ref.ID == "" || ref.Type == "" {
			return
		}
		if index, ok := seen[key]; ok {
			if ref.Rank > 0 && (refs[index].Rank <= 0 || ref.Rank < refs[index].Rank) {
				refs[index].Rank = ref.Rank
			}
			return
		}
		seen[key] = len(refs)
		refs = append(refs, ref)
	}
	for resultIndex, item := range res.Results {
		rank := item.Rank
		if rank <= 0 {
			rank = resultIndex + 1
		}
		if item.EvidenceID != "" {
			appendRef(domain.RecallFeedbackResultRef{
				Type:           domain.RecallFeedbackResultTypeEvidence,
				ID:             item.EvidenceID,
				Rank:           rank,
				Tier:           "evidence",
				StatusAtRecall: res.SearchState,
			})
		}
		for _, relationshipID := range item.RelationshipIDs {
			if relationshipID == "" {
				continue
			}
			appendRef(domain.RecallFeedbackResultRef{
				Type:           domain.RecallFeedbackResultTypeRelationship,
				ID:             relationshipID,
				Rank:           rank,
				Tier:           "relationship",
				StatusAtRecall: res.SearchState,
			})
		}
	}
	for _, community := range res.RelatedCommunities {
		appendRef(domain.RecallFeedbackResultRef{
			Type: domain.RecallFeedbackResultTypeCommunity, ID: community.CommunityID,
			Rank: community.Rank, StatusAtRecall: res.SearchState,
		})
		for _, relationship := range community.CommunityRelationships {
			appendRef(domain.RecallFeedbackResultRef{
				Type:           domain.RecallFeedbackResultTypeRelationship,
				ID:             relationship.RelationshipID,
				Rank:           community.Rank,
				StatusAtRecall: recallFeedbackFirstNonEmpty(relationship.SearchState, res.SearchState),
			})
		}
	}
	for rank, relationship := range res.RelatedRelationships {
		appendRef(domain.RecallFeedbackResultRef{
			Type:           domain.RecallFeedbackResultTypeRelationship,
			ID:             relationship.RelationshipID,
			Rank:           rank + 1,
			StatusAtRecall: relationship.SearchState,
		})
	}
	for rank, hypothesis := range res.RelatedHypotheses {
		appendRef(domain.RecallFeedbackResultRef{
			Type:           domain.RecallFeedbackResultTypeHypothesis,
			ID:             hypothesis.HypothesisID,
			Rank:           rank + 1,
			StatusAtRecall: hypothesis.Status,
		})
	}
	return refs
}

func recallFeedbackFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type RecallFeedbackEventServiceImpl struct {
	repo      RecallFeedbackEventStore
	retention RecallFeedbackRetentionProvider
	resolver  RecallFeedbackResultResolver
	now       func() time.Time

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
}

var _ RecallFeedbackEventService = (*RecallFeedbackEventServiceImpl)(nil)

func NewRecallFeedbackEventService(
	repo RecallFeedbackEventStore,
	retention RecallFeedbackRetentionProvider,
	resolver RecallFeedbackResultResolver,
) *RecallFeedbackEventServiceImpl {
	return &RecallFeedbackEventServiceImpl{
		repo:      repo,
		retention: retention,
		resolver:  resolver,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (s *RecallFeedbackEventServiceImpl) RecordRecallSnapshot(ctx context.Context, event domain.RecallFeedbackEvent) error {
	if s == nil || s.repo == nil {
		return nil
	}
	event = s.enrich(ctx, event)
	if strings.TrimSpace(event.RecallID) == "" {
		return fmt.Errorf("recall feedback event recall_id is required")
	}
	var err error
	event, err = bindRecallFeedbackEventSpace(ctx, event)
	if err != nil {
		return err
	}
	event.SnapshotState = domain.RecallFeedbackSnapshotCaptured
	return s.repo.RecordSnapshot(ctx, event)
}

func (s *RecallFeedbackEventServiceImpl) RecordRecallFeedback(ctx context.Context, feedback domain.RecallFeedbackSubmission) error {
	if s == nil || s.repo == nil {
		return nil
	}
	now := s.now().UTC()
	recallID := strings.TrimSpace(feedback.RecallID)
	if recallID == "" {
		return fmt.Errorf("%w: recall feedback event recall_id is required", ErrRecallFeedbackInvalidInput)
	}
	existing, err := s.repo.Get(ctx, recallID)
	if err != nil {
		return err
	}
	if existing == nil || !recallFeedbackEventAuthorizedForSubmission(ctx, existing) {
		return recallcontract.ErrRecallFeedbackEventNotFound
	}
	if existing.SnapshotState != domain.RecallFeedbackSnapshotCaptured {
		return fmt.Errorf("%w: recall feedback event snapshot is required", ErrRecallFeedbackInvalidInput)
	}
	if err := validateRecallFeedbackSubmissionRefs(feedback, existing.ResultRefs); err != nil {
		return fmt.Errorf("%w: %w: %w", ErrRecallFeedbackInvalidInput, ErrRecallFeedbackInvalidResultRef, err)
	}
	used := feedback.Used
	answerSupported := feedback.AnswerSupported
	missingContext := feedback.MissingContext
	irrelevant := feedback.Irrelevant
	event := *existing
	event = s.enrich(ctx, event)
	event.UpdatedAt = now
	event.FeedbackAt = &now
	event.SnapshotState = domain.RecallFeedbackSnapshotCaptured
	event.Used = &used
	event.AnswerSupported = &answerSupported
	event.Quality = strings.ToLower(strings.TrimSpace(feedback.Quality))
	event.MissingContext = &missingContext
	event.Irrelevant = &irrelevant
	event.FeedbackComment = strings.TrimSpace(feedback.FeedbackComment)
	event.IrrelevantRefs = feedback.IrrelevantRefs
	event.DreamFeedback = feedback.DreamFeedback
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	return s.repo.RecordFeedback(ctx, event)
}

func (s *RecallFeedbackEventServiceImpl) ListRecallFeedbackEvents(ctx context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("recall feedback event service unavailable")
	}
	return s.repo.List(ctx, filter)
}

func (s *RecallFeedbackEventServiceImpl) GetRecallFeedbackEvent(ctx context.Context, recallID string) (*domain.RecallFeedbackEvent, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("recall feedback event service unavailable")
	}
	event, err := s.repo.Get(ctx, recallID)
	if err != nil || event == nil {
		return event, err
	}
	if s.resolver != nil && len(event.ResultRefs) > 0 {
		scopeID := ""
		if event.TeamID != nil {
			scopeID = event.TeamID.String()
		} else if event.ProfileID != nil {
			scopeID = event.ProfileID.String()
		}
		if scopeID != "" {
			resolved, resolveErr := s.resolver.ResolveRecallFeedbackResults(ctx, scopeID, event.ResultRefs)
			if resolveErr != nil {
				return nil, resolveErr
			}
			event.ResolvedResults = resolved
		}
	}
	return event, nil
}

func (s *RecallFeedbackEventServiceImpl) Prune(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	days := DefaultRecallFeedbackRetentionDays
	if s.retention != nil {
		cfg, err := s.retention.RecallFeedbackRuntimeConfig(ctx)
		if err != nil {
			return fmt.Errorf("failed to read recall feedback retention config: %w", err)
		}
		if cfg.RetentionDays <= 0 {
			return fmt.Errorf("invalid recall feedback retention days: %d", cfg.RetentionDays)
		}
		days = cfg.RetentionDays
	}
	cutoff := s.now().UTC().AddDate(0, 0, -days)
	return s.repo.PruneBefore(ctx, cutoff)
}

func (s *RecallFeedbackEventServiceImpl) Start(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	go s.run(runCtx, done)
}

func (s *RecallFeedbackEventServiceImpl) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *RecallFeedbackEventServiceImpl) enrich(ctx context.Context, event domain.RecallFeedbackEvent) domain.RecallFeedbackEvent {
	now := s.now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.UpdatedAt.IsZero() {
		event.UpdatedAt = now
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok {
		if event.TeamID == nil && actor.TeamID != uuid.Nil {
			event.TeamID = &actor.TeamID
		}
		if event.ProfileID == nil && actor.OwnerID != uuid.Nil {
			event.ProfileID = &actor.OwnerID
		}
	}
	if actor, ok := requestctx.ActorFromContext(ctx); ok {
		if event.KeyID == nil && actor.CredentialID != nil {
			credentialID := *actor.CredentialID
			event.KeyID = &credentialID
		}
		if event.AuthMethod == "" {
			event.AuthMethod = actor.AuthMethod
		}
	}
	if event.ToolName == "" {
		event.ToolName = "recall_memory"
	}
	return event
}

func recallFeedbackEventAuthorizedForSubmission(ctx context.Context, event *domain.RecallFeedbackEvent) bool {
	if event == nil || event.TeamID == nil || event.SpaceID == nil || event.SpaceGeneration <= 0 {
		return false
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil {
		return false
	}
	if *event.TeamID != actor.TeamID {
		return false
	}
	for _, space := range actor.AllowedSpaces {
		if space.ID == *event.SpaceID && space.Generation == event.SpaceGeneration {
			return true
		}
	}
	return false
}

func bindRecallFeedbackEventSpace(ctx context.Context, event domain.RecallFeedbackEvent) (domain.RecallFeedbackEvent, error) {
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil {
		if event.TeamID == nil || event.SpaceID == nil || event.SpaceGeneration <= 0 {
			return event, fmt.Errorf("recall feedback event memory-space context is required")
		}
		return event, nil
	}
	if event.TeamID == nil {
		teamID := actor.TeamID
		event.TeamID = &teamID
	} else if *event.TeamID != actor.TeamID {
		return event, fmt.Errorf("recall feedback event memory-space context is outside the authenticated team")
	}

	if event.SpaceID != nil {
		var matched *domain.MemorySpaceAccess
		for index := range actor.AllowedSpaces {
			candidate := &actor.AllowedSpaces[index]
			if candidate.ID != *event.SpaceID || candidate.Generation <= 0 {
				continue
			}
			if matched != nil && matched.Generation != candidate.Generation {
				return event, fmt.Errorf("recall feedback event memory-space context is ambiguous")
			}
			matched = candidate
		}
		if matched == nil || (event.SpaceGeneration > 0 && event.SpaceGeneration != matched.Generation) {
			return event, fmt.Errorf("recall feedback event memory-space context is not authorized")
		}
		event.SpaceGeneration = matched.Generation
		return event, nil
	}

	privateSpaces, valid := uniqueRecallFeedbackSpaces(actor.AllowedSpaces, true)
	if !valid {
		return event, fmt.Errorf("recall feedback event memory-space context is ambiguous")
	}
	selected := privateSpaces
	if len(selected) == 0 {
		selected, valid = uniqueRecallFeedbackSpaces(actor.AllowedSpaces, false)
		if !valid {
			return event, fmt.Errorf("recall feedback event memory-space context is ambiguous")
		}
	}
	if len(selected) != 1 {
		return event, fmt.Errorf("recall feedback event memory-space context is missing or ambiguous")
	}
	spaceID := selected[0].ID
	event.SpaceID = &spaceID
	event.SpaceGeneration = selected[0].Generation
	return event, nil
}

func uniqueRecallFeedbackSpaces(spaces []domain.MemorySpaceAccess, private bool) ([]domain.MemorySpaceAccess, bool) {
	unique := make(map[uuid.UUID]domain.MemorySpaceAccess)
	for _, space := range spaces {
		isPrivate := space.Kind == domain.MemorySpaceProfilePrivate || space.Kind == domain.MemorySpaceCredentialPrivate
		if isPrivate != private || space.ID == uuid.Nil || space.Generation <= 0 {
			continue
		}
		if existing, ok := unique[space.ID]; ok && existing.Generation != space.Generation {
			return nil, false
		}
		unique[space.ID] = space
	}
	result := make([]domain.MemorySpaceAccess, 0, len(unique))
	for _, space := range unique {
		result = append(result, space)
	}
	return result, true
}

func validateRecallFeedbackSubmissionRefs(feedback domain.RecallFeedbackSubmission, returned []domain.RecallFeedbackResultRef) error {
	lookup := recallFeedbackReturnedRefSet(returned)
	seen := map[string]struct{}{}
	for _, ref := range feedback.IrrelevantRefs {
		key := recallFeedbackRefKey(ref.Type, ref.ID)
		if key == "" {
			return fmt.Errorf("recall feedback result ref was not returned by recall event")
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("recall feedback result refs must not contain duplicates")
		}
		seen[key] = struct{}{}
		if !lookup.contains(ref.Type, ref.ID, ref.Rank) {
			return fmt.Errorf("recall feedback result ref was not returned by recall event")
		}
	}
	seenHypotheses := map[string]struct{}{}
	for _, item := range feedback.DreamFeedback {
		key := recallFeedbackRefKey(domain.RecallFeedbackResultTypeHypothesis, item.DreamID)
		if key == "" {
			return fmt.Errorf("recall feedback hypothesis ref was not returned by recall event")
		}
		if _, ok := seenHypotheses[key]; ok {
			return fmt.Errorf("recall feedback hypothesis refs must not contain duplicates")
		}
		seenHypotheses[key] = struct{}{}
		if !lookup.contains(domain.RecallFeedbackResultTypeHypothesis, item.DreamID, 0) {
			return fmt.Errorf("recall feedback hypothesis ref was not returned by recall event")
		}
	}
	return nil
}

type recallFeedbackReturnedRefs map[string]map[int]struct{}

func recallFeedbackReturnedRefSet(refs []domain.RecallFeedbackResultRef) recallFeedbackReturnedRefs {
	out := recallFeedbackReturnedRefs{}
	for _, ref := range refs {
		key := recallFeedbackRefKey(ref.Type, ref.ID)
		if key == "" {
			continue
		}
		ranks := out[key]
		if ranks == nil {
			ranks = map[int]struct{}{}
			out[key] = ranks
		}
		ranks[ref.Rank] = struct{}{}
	}
	return out
}

func (refs recallFeedbackReturnedRefs) contains(refType, id string, rank int) bool {
	key := recallFeedbackRefKey(refType, id)
	if key == "" {
		return false
	}
	ranks, ok := refs[key]
	if !ok {
		return false
	}
	if rank <= 0 {
		return true
	}
	_, ok = ranks[rank]
	return ok
}

func recallFeedbackRefKey(refType, id string) string {
	refType = recallFeedbackCanonicalResultType(refType)
	id = strings.TrimSpace(id)
	if refType == "" || id == "" {
		return ""
	}
	return refType + "\x00" + id
}

func recallFeedbackCanonicalResultType(refType string) string {
	switch strings.TrimSpace(refType) {
	case domain.RecallFeedbackResultTypeDream, domain.RecallFeedbackResultTypeHypothesis:
		return domain.RecallFeedbackResultTypeHypothesis
	case domain.RecallFeedbackResultTypeFragment,
		domain.RecallFeedbackResultTypeClaim,
		domain.RecallFeedbackResultTypeFact,
		domain.RecallFeedbackResultTypeEvidence,
		domain.RecallFeedbackResultTypeRelationship,
		domain.RecallFeedbackResultTypeEntity,
		domain.RecallFeedbackResultTypeValue,
		domain.RecallFeedbackResultTypeCommunity:
		return strings.TrimSpace(refType)
	default:
		return ""
	}
}

func (s *RecallFeedbackEventServiceImpl) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	_ = s.Prune(ctx)
	ticker := time.NewTicker(recallFeedbackEventPruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Prune(ctx)
		}
	}
}
