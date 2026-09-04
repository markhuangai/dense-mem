package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

const (
	EvidenceConflictDefaultLimit      = 25
	EvidenceConflictMaxLimit          = 100
	EvidenceConflictDefaultEventLimit = 50
	EvidenceConflictMaxEventLimit     = 100
	EvidenceConflictMaxResults        = assessor.SemanticAssessmentMaxEvidenceConflictResults
	EvidenceConflictMaxPositions      = assessor.SemanticAssessmentMaxEvidenceConflictPositions
	EvidenceConflictMaxQuoteRunes     = assessor.SemanticAssessmentMaxEvidenceConflictQuoteRunes
)

var (
	ErrEvidenceConflictNotFound       = errors.New("evidence conflict not found")
	ErrEvidenceConflictVersionStale   = errors.New("evidence conflict version is stale")
	ErrEvidenceConflictNotOpen        = errors.New("evidence conflict is not open")
	ErrEvidenceConflictInvalidCommand = errors.New("evidence conflict command is invalid")
	ErrEvidenceConflictStaleInput     = errors.New("evidence conflict citation is stale")
)

// EvidenceConflictPositionRecord is the immutable read model for one exact
// cited occurrence span. It intentionally carries both canonical and
// occurrence ownership so cross-profile reuse remains auditable.
type EvidenceConflictPositionRecord struct {
	ConflictID               string    `json:"conflict_id,omitempty"`
	PositionID               string    `json:"position_id"`
	PositionKey              string    `json:"position_key,omitempty"`
	CanonicalEvidenceID      string    `json:"evidence_id"`
	CanonicalOwnerProfileID  string    `json:"canonical_owner_profile_id,omitempty"`
	OccurrenceID             string    `json:"occurrence_id"`
	OccurrenceOwnerProfileID string    `json:"occurrence_owner_profile_id,omitempty"`
	Quote                    string    `json:"quote"`
	SpanStart                int       `json:"span_start"`
	SpanEnd                  int       `json:"span_end"`
	Authority                string    `json:"authority"`
	Submitted                bool      `json:"submitted"`
	CreatedAt                time.Time `json:"created_at"`
}

type EvidenceConflictEventRecord struct {
	ConflictEventID     string                           `json:"event_id"`
	ConflictID          string                           `json:"conflict_id"`
	Ordinal             int64                            `json:"ordinal"`
	Action              string                           `json:"action"`
	StatusAfter         string                           `json:"status_after"`
	CaseVersion         int                              `json:"case_version"`
	ActorKind           string                           `json:"actor_kind"`
	ActorID             string                           `json:"actor_id,omitempty"`
	Reason              string                           `json:"reason,omitempty"`
	PreferredPositionID string                           `json:"preferred_position_id,omitempty"`
	CitationSnapshot    []EvidenceConflictPositionRecord `json:"citation_snapshot"`
	CreatedAt           time.Time                        `json:"created_at"`
}

type EvidenceConflictCaseRecord struct {
	TeamID              string                           `json:"team_id"`
	ConflictID          string                           `json:"conflict_id"`
	SpaceID             string                           `json:"space_id"`
	SpaceGeneration     int64                            `json:"space_generation"`
	CaseKey             string                           `json:"-"`
	Kind                string                           `json:"kind"`
	Status              string                           `json:"status"`
	Version             int                              `json:"version"`
	PreferredPositionID string                           `json:"preferred_position_id,omitempty"`
	ResolvedAt          *time.Time                       `json:"resolved_at,omitempty"`
	ResolutionReason    string                           `json:"resolution_reason,omitempty"`
	CreatedAt           time.Time                        `json:"created_at"`
	UpdatedAt           time.Time                        `json:"updated_at"`
	Positions           []EvidenceConflictPositionRecord `json:"positions"`
	Events              []EvidenceConflictEventRecord    `json:"events,omitempty"`
}

type EvidenceConflictListInput struct {
	TeamID string
	Status string
	Limit  int
	Cursor *EvidenceConflictCursor
}

type EvidenceConflictListResult struct {
	Items      []EvidenceConflictCaseRecord
	NextCursor *EvidenceConflictCursor
}

type EvidenceConflictCursor struct {
	Version      int       `json:"version"`
	TeamID       string    `json:"team_id"`
	StatusFilter string    `json:"status_filter"`
	UpdatedAt    time.Time `json:"updated_at"`
	ConflictID   string    `json:"conflict_id"`
}

func EncodeEvidenceConflictCursor(cursor EvidenceConflictCursor) (string, error) {
	if err := cursor.validate(cursor.TeamID, cursor.StatusFilter); err != nil {
		return "", err
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode evidence conflict cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecodeEvidenceConflictCursor(raw string) (*EvidenceConflictCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 1024 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) == 0 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	var cursor EvidenceConflictCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if err := cursor.validate(cursor.TeamID, cursor.StatusFilter); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func (c EvidenceConflictCursor) validate(teamID, status string) error {
	if c.Version != 1 || c.UpdatedAt.IsZero() {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.TeamID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.ConflictID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if c.StatusFilter != "" && !validEvidenceConflictStatus(c.StatusFilter) {
		return ErrEvidenceConflictInvalidCommand
	}
	if strings.TrimSpace(teamID) != "" && c.TeamID != strings.TrimSpace(teamID) {
		return ErrEvidenceConflictInvalidCommand
	}
	if c.StatusFilter != strings.TrimSpace(status) {
		return ErrEvidenceConflictInvalidCommand
	}
	return nil
}

type EvidenceConflictGetInput struct {
	TeamID      string
	ConflictID  string
	EventLimit  int
	EventCursor *EvidenceConflictEventCursor
}

type EvidenceConflictEventCursor struct {
	Version    int    `json:"version"`
	TeamID     string `json:"team_id"`
	ConflictID string `json:"conflict_id"`
	Ordinal    int64  `json:"ordinal"`
	EventID    string `json:"event_id"`
}

type EvidenceConflictGetResult struct {
	Conflict        *EvidenceConflictCaseRecord
	NextEventCursor *EvidenceConflictEventCursor
}

func EncodeEvidenceConflictEventCursor(cursor EvidenceConflictEventCursor) (string, error) {
	if err := cursor.validate(cursor.TeamID, cursor.ConflictID); err != nil {
		return "", err
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode evidence conflict event cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecodeEvidenceConflictEventCursor(raw string) (*EvidenceConflictEventCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 1024 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) == 0 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	var cursor EvidenceConflictEventCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if err := cursor.validate(cursor.TeamID, cursor.ConflictID); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func (c EvidenceConflictEventCursor) validate(teamID, conflictID string) error {
	if c.Version != 1 || c.Ordinal < 1 {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.TeamID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.ConflictID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.EventID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if strings.TrimSpace(teamID) != "" && c.TeamID != strings.TrimSpace(teamID) {
		return ErrEvidenceConflictInvalidCommand
	}
	if strings.TrimSpace(conflictID) != "" && c.ConflictID != strings.TrimSpace(conflictID) {
		return ErrEvidenceConflictInvalidCommand
	}
	return nil
}

type EvidenceConflictResolutionInput struct {
	TeamID              string
	ConflictID          string
	ExpectedVersion     int
	Decision            string
	Reason              string
	PreferredPositionID string
	ActorKind           string
	ActorID             string
}

type EvidenceConflictRepository interface {
	ListEvidenceConflicts(context.Context, EvidenceConflictListInput) (*EvidenceConflictListResult, error)
	GetEvidenceConflict(context.Context, EvidenceConflictGetInput) (*EvidenceConflictGetResult, error)
	ResolveEvidenceConflict(context.Context, EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error)
}

var _ EvidenceConflictRepository = (*LedgerRepositoryImpl)(nil)

type resolvedEvidenceConflictCitation struct {
	CanonicalEvidenceID      string
	CanonicalOwnerProfileID  string
	OccurrenceID             string
	OccurrenceOwnerProfileID string
	FragmentContent          string
	FragmentContentHash      string
	Content                  string
	ContentHash              string
	Authority                string
	SourceID                 string
	SourceRevisionID         string
	CurrentSourceRevisionID  string
	SpaceID                  string
	SpaceGeneration          int64
	Submitted                bool
	Known                    bool
}

type evidenceConflictCitation struct {
	EvidenceID string
	Start      int
	End        int
}

func evidenceConflictPositionKey(citation resolvedEvidenceConflictCitation, start, end int) string {
	return sha256LengthDelimited(
		citation.CanonicalEvidenceID,
		citation.ContentHash,
		fmt.Sprintf("%d", start),
		fmt.Sprintf("%d", end),
	)
}

func evidenceConflictCaseKey(teamID, spaceID string, generation int64, positionKeys []string) string {
	keys := append([]string(nil), positionKeys...)
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+3)
	parts = append(parts, teamID, spaceID, fmt.Sprintf("%d", generation))
	parts = append(parts, keys...)
	return sha256LengthDelimited(parts...)
}

func sha256LengthDelimited(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		var length [8]byte
		length[0] = byte(len(part) >> 56)
		length[1] = byte(len(part) >> 48)
		length[2] = byte(len(part) >> 40)
		length[3] = byte(len(part) >> 32)
		length[4] = byte(len(part) >> 24)
		length[5] = byte(len(part) >> 16)
		length[6] = byte(len(part) >> 8)
		length[7] = byte(len(part))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func evidenceConflictQuote(content string, start, end int) (string, error) {
	if start < 0 || end <= start {
		return "", errors.New("evidence conflict span is invalid")
	}
	runes := []rune(content)
	if end > len(runes) {
		return "", errors.New("evidence conflict span is outside evidence")
	}
	quote := string(runes[start:end])
	if quote == "" || !utf8.ValidString(quote) {
		return "", errors.New("evidence conflict quote is invalid")
	}
	if len(runes[start:end]) > EvidenceConflictMaxQuoteRunes {
		return "", fmt.Errorf("evidence conflict quote must contain at most %d runes", EvidenceConflictMaxQuoteRunes)
	}
	return quote, nil
}

func evidenceConflictResolvedPosition(citation evidenceConflictCitation, resolved resolvedEvidenceConflictCitation) (EvidenceConflictPositionRecord, error) {
	quote, err := evidenceConflictQuote(resolved.Content, citation.Start, citation.End)
	if err != nil {
		return EvidenceConflictPositionRecord{}, err
	}
	return EvidenceConflictPositionRecord{
		PositionKey:              evidenceConflictPositionKey(resolved, citation.Start, citation.End),
		CanonicalEvidenceID:      resolved.CanonicalEvidenceID,
		CanonicalOwnerProfileID:  resolved.CanonicalOwnerProfileID,
		OccurrenceID:             resolved.OccurrenceID,
		OccurrenceOwnerProfileID: resolved.OccurrenceOwnerProfileID,
		Quote:                    quote,
		SpanStart:                citation.Start,
		SpanEnd:                  citation.End,
		Authority:                resolved.Authority,
		Submitted:                resolved.Submitted,
	}, nil
}

func validateEvidenceConflictResultInputs(input CommitSubmissionAssessmentInput) error {
	if len(input.EvidenceConflictResults) > EvidenceConflictMaxResults {
		return fmt.Errorf("submission evidence conflicts must contain at most %d results", EvidenceConflictMaxResults)
	}
	for index, conflict := range input.EvidenceConflictResults {
		if len(conflict.Positions) < 2 || len(conflict.Positions) > EvidenceConflictMaxPositions {
			return fmt.Errorf("submission evidence conflict[%d] must contain between 2 and %d positions", index, EvidenceConflictMaxPositions)
		}
		seen := make(map[string]struct{}, len(conflict.Positions))
		for positionIndex, position := range conflict.Positions {
			position.EvidenceID = strings.TrimSpace(position.EvidenceID)
			if position.EvidenceID == "" || position.Start < 0 || position.End <= position.Start {
				return fmt.Errorf("submission evidence conflict[%d] position[%d] is invalid", index, positionIndex)
			}
			key := fmt.Sprintf("%s:%d:%d", position.EvidenceID, position.Start, position.End)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("submission evidence conflict[%d] contains a duplicate position", index)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func (r *LedgerRepositoryImpl) commitEvidenceConflictsInTx(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	evidence []EvidenceFragment,
) error {
	if len(input.Commit.EvidenceConflictResults) == 0 {
		return nil
	}
	if err := validateEvidenceConflictResultInputs(input.Commit); err != nil {
		return err
	}
	resolved, err := r.resolveRememberConflictCitations(ctx, tx, input, evidence)
	if err != nil {
		return err
	}
	caseInputs := make([][]EvidenceConflictPositionRecord, 0, len(input.Commit.EvidenceConflictResults))
	caseKeys := make([]string, 0, len(input.Commit.EvidenceConflictResults))
	for _, conflict := range input.Commit.EvidenceConflictResults {
		positions := make([]EvidenceConflictPositionRecord, 0, len(conflict.Positions))
		positionKeys := make([]string, 0, len(conflict.Positions))
		seenKeys := make(map[string]struct{}, len(conflict.Positions))
		submitted := false
		for _, citation := range conflict.Positions {
			item, ok := resolved[citation.EvidenceID]
			if !ok {
				return ErrEvidenceConflictStaleInput
			}
			position, err := evidenceConflictResolvedPosition(evidenceConflictCitation(citation), item)
			if err != nil {
				return err
			}
			if position.Submitted {
				submitted = true
			}
			if _, exists := seenKeys[position.PositionKey]; exists {
				return errors.New("submission evidence conflict contains duplicate canonical positions")
			}
			seenKeys[position.PositionKey] = struct{}{}
			positionKeys = append(positionKeys, position.PositionKey)
			positions = append(positions, position)
		}
		if !submitted {
			return errors.New("submission evidence conflict must contain a submitted position")
		}
		sort.Slice(positions, func(i, j int) bool { return positions[i].PositionKey < positions[j].PositionKey })
		sort.Strings(positionKeys)
		caseInputs = append(caseInputs, positions)
		caseKeys = append(caseKeys, evidenceConflictCaseKey(input.TeamID, input.SpaceID, input.SpaceGeneration, positionKeys))
	}
	seenCaseKeys := make(map[string]struct{}, len(caseKeys))
	for _, caseKey := range caseKeys {
		if _, exists := seenCaseKeys[caseKey]; exists {
			return errors.New("submission evidence conflict contains duplicate position sets")
		}
		seenCaseKeys[caseKey] = struct{}{}
	}
	orderedCaseKeys := make([]string, 0, len(seenCaseKeys))
	for caseKey := range seenCaseKeys {
		orderedCaseKeys = append(orderedCaseKeys, caseKey)
	}
	if err := lockEvidenceConflictCaseKeys(ctx, tx, input.TeamID, input.SpaceID, input.SpaceGeneration, orderedCaseKeys); err != nil {
		return err
	}
	for index, caseKey := range caseKeys {
		if err := r.upsertRememberEvidenceConflictCase(ctx, tx, input, caseKey, caseInputs[index]); err != nil {
			return err
		}
	}
	return nil
}

func (r *LedgerRepositoryImpl) resolveRememberConflictCitations(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	evidence []EvidenceFragment,
) (map[string]resolvedEvidenceConflictCitation, error) {
	byID := make(map[string]resolvedEvidenceConflictCitation, len(evidence)+len(input.Commit.KnownEvidenceSnapshot))
	for index, item := range evidence {
		if strings.TrimSpace(item.SubmittedFragmentID) == "" {
			continue
		}
		citation := resolvedEvidenceConflictCitation{
			CanonicalEvidenceID: item.FragmentID, CanonicalOwnerProfileID: item.CanonicalOwnerID,
			OccurrenceID: item.OccurrenceID, OccurrenceOwnerProfileID: item.OccurrenceOwnerID,
			Content: item.Content, ContentHash: item.ContentHash, Authority: item.Authority, Submitted: true,
		}
		byID[item.SubmittedFragmentID] = citation
		if index < len(input.Commit.Items) && strings.TrimSpace(input.Commit.Items[index].EvidenceID) != "" {
			byID[input.Commit.Items[index].EvidenceID] = citation
		}
	}
	citedEvidenceIDs := make(map[string]struct{})
	for _, conflict := range input.Commit.EvidenceConflictResults {
		for _, position := range conflict.Positions {
			citedEvidenceIDs[position.EvidenceID] = struct{}{}
		}
	}
	external := make(map[string]externalEvidenceConflictCitationRequest, len(citedEvidenceIDs))
	for index := range input.Commit.KnownEvidenceSnapshot {
		snapshot := &input.Commit.KnownEvidenceSnapshot[index]
		if _, cited := citedEvidenceIDs[snapshot.EvidenceID]; !cited {
			continue
		}
		if strings.TrimSpace(snapshot.EvidenceID) == "" {
			return nil, ErrEvidenceConflictStaleInput
		}
		external[snapshot.EvidenceID] = externalEvidenceConflictCitationRequest{EvidenceID: snapshot.EvidenceID, OwnerID: snapshot.OwnerProfileID, Snapshot: snapshot}
	}
	for submittedID, candidateIDs := range input.Commit.EvidenceConflictCandidateEvidenceIDs {
		for _, candidateID := range candidateIDs {
			if _, cited := citedEvidenceIDs[candidateID]; !cited {
				continue
			}
			if _, exists := byID[candidateID]; exists {
				continue
			}
			if _, exists := external[candidateID]; !exists {
				external[candidateID] = externalEvidenceConflictCitationRequest{EvidenceID: candidateID}
			}
		}
		_ = submittedID
	}
	if len(external) > 0 {
		requests := make([]externalEvidenceConflictCitationRequest, 0, len(external))
		for _, request := range external {
			requests = append(requests, request)
		}
		items, err := loadAndLockEvidenceConflictCitations(ctx, tx, input, requests)
		if err != nil {
			return nil, err
		}
		for evidenceID, item := range items {
			byID[evidenceID] = item
		}
	}
	for _, conflict := range input.Commit.EvidenceConflictResults {
		submittedIDs := make(map[string]struct{})
		for _, position := range conflict.Positions {
			if item, ok := byID[position.EvidenceID]; ok && item.Submitted {
				submittedIDs[position.EvidenceID] = struct{}{}
			}
		}
		for _, position := range conflict.Positions {
			item, ok := byID[position.EvidenceID]
			if !ok {
				return nil, ErrEvidenceConflictStaleInput
			}
			if !item.Submitted && !item.Known {
				associated := false
				for submittedID, candidates := range input.Commit.EvidenceConflictCandidateEvidenceIDs {
					if _, exists := submittedIDs[submittedID]; !exists {
						continue
					}
					for _, candidateID := range candidates {
						if candidateID == position.EvidenceID {
							associated = true
							break
						}
					}
					if associated {
						break
					}
				}
				if !associated {
					return nil, ErrEvidenceConflictStaleInput
				}
			}
		}
	}
	return byID, nil
}

type evidenceConflictCitationMetadata struct {
	EvidenceID   string
	OwnerID      string
	SpaceID      string
	SourceID     string
	OccurrenceID string
}

type externalEvidenceConflictCitationRequest struct {
	EvidenceID string
	OwnerID    string
	Snapshot   *SubmissionAssessmentKnownEvidence
}

func loadEvidenceConflictCitationMetadata(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	requestID string,
) (evidenceConflictCitationMetadata, error) {
	var metadata evidenceConflictCitationMetadata
	err := tx.WithContext(ctx).Raw(`
		SELECT fragment.fragment_id::text, fragment.owner_profile_id::text,
		       fragment.space_id::text, COALESCE(fragment.source_id::text, ''),
		       occurrence.occurrence_id::text
		FROM evidence_fragments AS fragment
		JOIN evidence_occurrences AS occurrence
		  ON occurrence.team_id = fragment.team_id
		 AND occurrence.canonical_fragment_id = fragment.fragment_id
		 AND occurrence.canonical_owner_profile_id = fragment.owner_profile_id
		 AND occurrence.occurrence_id = fragment.fragment_id
		WHERE fragment.team_id = ?::uuid AND fragment.fragment_id = ?::uuid
	`, input.TeamID, requestID).Row().Scan(&metadata.EvidenceID, &metadata.OwnerID, &metadata.SpaceID, &metadata.SourceID, &metadata.OccurrenceID)
	if errors.Is(err, sql.ErrNoRows) {
		return evidenceConflictCitationMetadata{}, ErrEvidenceConflictStaleInput
	}
	if err != nil {
		return evidenceConflictCitationMetadata{}, err
	}
	return metadata, nil
}

func lockEvidenceConflictCitationRows(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput, requests []externalEvidenceConflictCitationRequest) error {
	// Canonical evidence tables are append-only under profile RLS; lifecycle advisory locks fence the mutable eligibility predicates.
	metadata := make(map[string]evidenceConflictCitationMetadata, len(requests))
	for _, request := range requests {
		item, err := loadEvidenceConflictCitationMetadata(ctx, tx, input, request.EvidenceID)
		if err != nil {
			return err
		}
		if request.Snapshot != nil && strings.TrimSpace(request.Snapshot.OwnerProfileID) != item.OwnerID {
			return ErrEvidenceConflictStaleInput
		}
		metadata[request.EvidenceID] = item
	}
	spaceIDs := make([]string, 0, len(metadata))
	seenSpaces := make(map[string]struct{}, len(metadata))
	sourceKeys := make([]string, 0, len(metadata))
	sources := make(map[string]struct{ sourceID, ownerID string }, len(metadata))
	fragmentIDs := make([]string, 0, len(metadata))
	for _, item := range metadata {
		if _, exists := seenSpaces[item.SpaceID]; !exists {
			seenSpaces[item.SpaceID] = struct{}{}
			spaceIDs = append(spaceIDs, item.SpaceID)
		}
		if item.SourceID != "" {
			key := item.SourceID + "\x00" + item.OwnerID
			if _, exists := sources[key]; !exists {
				sources[key] = struct{ sourceID, ownerID string }{sourceID: item.SourceID, ownerID: item.OwnerID}
				sourceKeys = append(sourceKeys, key)
			}
		}
		fragmentIDs = append(fragmentIDs, item.EvidenceID)
	}
	sort.Strings(spaceIDs)
	for _, spaceID := range spaceIDs {
		if err := lockKnownEvidenceSpace(ctx, tx, input.TeamID, spaceID); err != nil {
			if errors.Is(err, sql.ErrNoRows) || isPostgresLockNotAvailable(err) {
				return ErrEvidenceConflictStaleInput
			}
			return err
		}
	}
	sort.Strings(sourceKeys)
	for _, key := range sourceKeys {
		source := sources[key]
		if err := lockKnownEvidenceSource(ctx, tx, input.TeamID, source.sourceID, source.ownerID); err != nil {
			if errors.Is(err, sql.ErrNoRows) || isPostgresLockNotAvailable(err) {
				return ErrEvidenceConflictStaleInput
			}
			return err
		}
	}
	sort.Strings(fragmentIDs)
	if err := lockEvidenceLifecycleTargetIDs(ctx, tx, input.TeamID, fragmentIDs); err != nil {
		return err
	}
	return nil
}

func loadAndLockEvidenceConflictCitations(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	requests []externalEvidenceConflictCitationRequest,
) (map[string]resolvedEvidenceConflictCitation, error) {
	if err := lockEvidenceConflictCitationRows(ctx, tx, input, requests); err != nil {
		return nil, err
	}
	items := make(map[string]resolvedEvidenceConflictCitation, len(requests))
	for _, request := range requests {
		item, err := loadEvidenceConflictCitationByID(ctx, tx, input, request.EvidenceID, request.OwnerID, request.Snapshot != nil)
		if err != nil {
			return nil, err
		}
		if request.Snapshot != nil && !evidenceConflictCitationMatchesKnownSnapshot(item, *request.Snapshot) {
			return nil, ErrEvidenceConflictStaleInput
		}
		items[request.EvidenceID] = item
	}
	return items, nil
}

func evidenceConflictCitationMatchesKnownSnapshot(item resolvedEvidenceConflictCitation, expected SubmissionAssessmentKnownEvidence) bool {
	return item.CanonicalEvidenceID == expected.EvidenceID &&
		item.CanonicalEvidenceID == expected.FragmentID &&
		item.CanonicalOwnerProfileID == expected.OwnerProfileID &&
		item.FragmentContent == expected.Content &&
		item.FragmentContentHash == expected.ContentHash &&
		item.Authority == expected.Authority &&
		item.SourceID == expected.SourceID &&
		item.SourceRevisionID == expected.SourceRevisionID &&
		item.CurrentSourceRevisionID == expected.CurrentSourceRevisionID &&
		item.SpaceID == expected.SpaceID &&
		item.SpaceGeneration == expected.SpaceGeneration
}

func loadEvidenceConflictCitationByID(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	evidenceID, expectedOwner string,
	known bool,
) (resolvedEvidenceConflictCitation, error) {
	spaceID := strings.TrimSpace(input.SpaceID)
	if spaceID == "" {
		return resolvedEvidenceConflictCitation{}, ErrEvidenceConflictStaleInput
	}
	args := []any{input.TeamID, evidenceID, spaceID, input.SpaceGeneration}
	ownerClause := ""
	if strings.TrimSpace(expectedOwner) != "" {
		ownerClause = " AND fragment.owner_profile_id = ?::uuid"
		args = append(args, expectedOwner)
	}
	query := `
		SELECT fragment.fragment_id::text, fragment.owner_profile_id::text,
		       fragment.content, fragment.content_hash, fragment.authority,
		       COALESCE(fragment.source_id::text, ''), COALESCE(fragment.source_revision_id::text, ''),
		       COALESCE(source.current_revision_id::text, ''), fragment.space_id::text, fragment.space_generation,
		       occurrence.occurrence_id::text, occurrence.owner_profile_id::text,
		       occurrence.content, occurrence.content_hash
		FROM evidence_fragments AS fragment
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
		JOIN memory_spaces AS space
		  ON space.team_id = fragment.team_id AND space.id = fragment.space_id
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		 AND source.owner_profile_id = fragment.owner_profile_id
		JOIN evidence_occurrences AS occurrence
		  ON occurrence.team_id = fragment.team_id
		 AND occurrence.canonical_fragment_id = fragment.fragment_id
		 AND occurrence.canonical_owner_profile_id = fragment.owner_profile_id
		 AND occurrence.occurrence_id = fragment.fragment_id
		WHERE fragment.team_id = ?::uuid
		  AND fragment.fragment_id = ?::uuid
		  AND fragment.space_id = ?::uuid
		  AND fragment.space_generation = ?
		  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
		  AND ingest.status = 'completed'
		  AND space.lifecycle_state = 'active'
		  AND (space.kind = 'team_shared' OR dense_mem_space_allowed(space.id))
		  AND NOT EXISTS (SELECT 1 FROM evidence_exact_aliases alias WHERE alias.team_id = fragment.team_id AND alias.alias_fragment_id = fragment.fragment_id)
		  AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events lifecycle WHERE lifecycle.team_id = fragment.team_id AND lifecycle.target_fragment_id = fragment.fragment_id)
		  AND NOT EXISTS (SELECT 1 FROM evidence_quarantines quarantine WHERE quarantine.team_id = fragment.team_id AND quarantine.fragment_id = fragment.fragment_id AND quarantine.status = 'active')
		  AND (fragment.source_id IS NULL OR EXISTS (
		      SELECT 1 FROM evidence_sources source
		      WHERE source.team_id = fragment.team_id AND source.source_id = fragment.source_id
		        AND source.owner_profile_id = fragment.owner_profile_id AND source.current_revision_id = fragment.source_revision_id
		  ))` + ownerClause + `
			  AND NOT (ingest.source_summary = 'overdue conflict deletion-only derivation' AND ingest.metadata ->> 'conflict_resolution_deletion_only' = 'true')`
	var item resolvedEvidenceConflictCitation
	err := tx.WithContext(ctx).Raw(query, args...).Row().Scan(
		&item.CanonicalEvidenceID, &item.CanonicalOwnerProfileID, &item.FragmentContent,
		&item.FragmentContentHash, &item.Authority, &item.SourceID, &item.SourceRevisionID,
		&item.CurrentSourceRevisionID, &item.SpaceID, &item.SpaceGeneration,
		&item.OccurrenceID, &item.OccurrenceOwnerProfileID, &item.Content, &item.ContentHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return resolvedEvidenceConflictCitation{}, ErrEvidenceConflictStaleInput
	}
	if err != nil {
		return resolvedEvidenceConflictCitation{}, err
	}
	if known && strings.TrimSpace(expectedOwner) == "" {
		return resolvedEvidenceConflictCitation{}, ErrEvidenceConflictStaleInput
	}
	item.Known = known
	return item, nil
}

func (r *LedgerRepositoryImpl) upsertRememberEvidenceConflictCase(
	ctx context.Context,
	tx *gorm.DB,
	input SynchronousRememberCommitInput,
	caseKey string,
	positions []EvidenceConflictPositionRecord,
) error {
	var conflictID, status, preferred string
	var version int
	err := tx.WithContext(ctx).Raw(`
		SELECT conflict_id::text, status, version, COALESCE(preferred_position_id::text, '')
		FROM evidence_conflict_cases
		WHERE team_id = ?::uuid AND space_id = ?::uuid AND space_generation = ? AND case_key = ?
		FOR UPDATE
	`, input.TeamID, input.SpaceID, input.SpaceGeneration, caseKey).Row().Scan(&conflictID, &status, &version, &preferred)
	if errors.Is(err, sql.ErrNoRows) {
		conflictID = uuid.NewString()
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO evidence_conflict_cases (team_id, conflict_id, space_id, space_generation, case_key, status, version)
			VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, 'open', 1)
		`, input.TeamID, conflictID, input.SpaceID, input.SpaceGeneration, caseKey).Error; err != nil {
			return err
		}
		for index := range positions {
			positions[index].ConflictID = conflictID
			positions[index].PositionID = uuid.NewString()
			if err := insertEvidenceConflictPosition(ctx, tx, input, positions[index]); err != nil {
				return err
			}
		}
		storedPositions, err := loadEvidenceConflictPositions(ctx, tx, input.TeamID, conflictID)
		if err != nil {
			return err
		}
		return insertEvidenceConflictEvent(ctx, tx, input, conflictID, 1, "opened", "open", 1, "profile", input.OwnerProfileID, "", "", storedPositions)
	}
	if err != nil {
		return err
	}
	storedPositions, err := loadEvidenceConflictPositions(ctx, tx, input.TeamID, conflictID)
	if err != nil {
		return err
	}
	storedPositionIDs := make(map[string]EvidenceConflictPositionRecord, len(storedPositions))
	for _, stored := range storedPositions {
		storedPositionIDs[stored.PositionKey] = stored
	}
	for index := range positions {
		stored, exists := storedPositionIDs[positions[index].PositionKey]
		if !exists {
			return errors.New("evidence conflict recurrence position set does not match the stored case")
		}
		positions[index].ConflictID = conflictID
		positions[index].PositionID = stored.PositionID
		positions[index].CreatedAt = stored.CreatedAt
	}
	if status == "open" {
		version++
		if err := tx.WithContext(ctx).Exec(`
			UPDATE evidence_conflict_cases SET version = ?, updated_at = now()
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND status = 'open' AND version = ?
		`, version, input.TeamID, conflictID, version-1).Error; err != nil {
			return err
		}
		ordinal, err := nextEvidenceConflictOrdinal(ctx, tx, input.TeamID, conflictID)
		if err != nil {
			return err
		}
		return insertEvidenceConflictEvent(ctx, tx, input, conflictID, ordinal, "recited", "open", version, "profile", input.OwnerProfileID, "", preferred, positions)
	}
	ordinal, err := nextEvidenceConflictOrdinal(ctx, tx, input.TeamID, conflictID)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		UPDATE evidence_conflict_cases SET updated_at = now()
		WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND status = ? AND version = ?
	`, input.TeamID, conflictID, status, version).Error; err != nil {
		return err
	}
	return insertEvidenceConflictEvent(ctx, tx, input, conflictID, ordinal, "recited", status, version, "profile", input.OwnerProfileID, "", preferred, positions)
}

func evidenceConflictCaseLockKey(teamID, spaceID string, generation int64, caseKey string) string {
	return strings.Join([]string{"evidence-conflict", teamID, spaceID, fmt.Sprintf("%d", generation), caseKey}, ":")
}

func lockEvidenceConflictCaseKeys(ctx context.Context, tx *gorm.DB, teamID, spaceID string, generation int64, caseKeys []string) error {
	ordered := append([]string(nil), caseKeys...)
	sort.Strings(ordered)
	for _, caseKey := range ordered {
		if err := tx.WithContext(ctx).Exec(
			"SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))",
			evidenceConflictCaseLockKey(teamID, spaceID, generation, caseKey),
		).Error; err != nil {
			return err
		}
	}
	return nil
}

func insertEvidenceConflictPosition(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput, position EvidenceConflictPositionRecord) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_conflict_positions (
			team_id, conflict_id, space_id, space_generation, position_id, position_key,
			canonical_evidence_id, canonical_owner_profile_id, occurrence_id, occurrence_owner_profile_id,
			quote, span_start, span_end, authority, submitted
		) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?)
	`, input.TeamID, position.ConflictID, input.SpaceID, input.SpaceGeneration, position.PositionID, position.PositionKey,
		position.CanonicalEvidenceID, position.CanonicalOwnerProfileID, position.OccurrenceID, position.OccurrenceOwnerProfileID,
		position.Quote, position.SpanStart, position.SpanEnd, position.Authority, position.Submitted).Error
}

func nextEvidenceConflictOrdinal(ctx context.Context, tx *gorm.DB, teamID, conflictID string) (int64, error) {
	var ordinal int64
	if err := tx.WithContext(ctx).Raw(`SELECT COALESCE(max(ordinal), 0) + 1 FROM evidence_conflict_events WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, conflictID).Row().Scan(&ordinal); err != nil {
		return 0, err
	}
	if ordinal < 1 {
		return 1, nil
	}
	return ordinal, nil
}

func insertEvidenceConflictEvent(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput, conflictID string, ordinal int64, action, status string, version int, actorKind, actorID, reason, preferred string, positions []EvidenceConflictPositionRecord) error {
	snapshot := make([]map[string]any, 0, len(positions))
	for _, position := range positions {
		snapshot = append(snapshot, map[string]any{
			"position_id": position.PositionID, "position_key": position.PositionKey,
			"evidence_id": position.CanonicalEvidenceID, "canonical_owner_profile_id": position.CanonicalOwnerProfileID,
			"occurrence_id": position.OccurrenceID, "occurrence_owner_profile_id": position.OccurrenceOwnerProfileID,
			"quote": position.Quote, "span_start": position.SpanStart, "span_end": position.SpanEnd,
			"authority": position.Authority, "submitted": position.Submitted, "created_at": position.CreatedAt,
		})
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_conflict_events (
			team_id, conflict_event_id, conflict_id, space_id, space_generation, ordinal,
			action, status_after, case_version, actor_kind, actor_id, reason, preferred_position_id, citation_snapshot
		) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, '')::uuid, ?::jsonb)
	`, input.TeamID, uuid.NewString(), conflictID, input.SpaceID, input.SpaceGeneration, ordinal, action, status, version, actorKind, actorID, reason, preferred, string(encoded)).Error
}

func validEvidenceConflictStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open", "resolved", "dismissed":
		return true
	default:
		return false
	}
}
