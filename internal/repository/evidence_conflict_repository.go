package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

const (
	EvidenceConflictDefaultLimit      = knowledgecontract.EvidenceConflictDefaultLimit
	EvidenceConflictMaxLimit          = knowledgecontract.EvidenceConflictMaxLimit
	EvidenceConflictDefaultEventLimit = knowledgecontract.EvidenceConflictDefaultEventLimit
	EvidenceConflictMaxEventLimit     = knowledgecontract.EvidenceConflictMaxEventLimit
	EvidenceConflictMaxResults        = knowledgecontract.EvidenceConflictMaxResults
	EvidenceConflictMaxPositions      = knowledgecontract.EvidenceConflictMaxPositions
	EvidenceConflictMaxQuoteRunes     = knowledgecontract.EvidenceConflictMaxQuoteRunes
)

var (
	ErrEvidenceConflictNotFound       = knowledgecontract.ErrEvidenceConflictNotFound
	ErrEvidenceConflictVersionStale   = knowledgecontract.ErrEvidenceConflictVersionStale
	ErrEvidenceConflictNotOpen        = knowledgecontract.ErrEvidenceConflictNotOpen
	ErrEvidenceConflictInvalidCommand = knowledgecontract.ErrEvidenceConflictInvalidCommand
	ErrEvidenceConflictStaleInput     = knowledgecontract.ErrEvidenceConflictStaleInput
)

type EvidenceConflictPositionRecord = knowledgecontract.EvidenceConflictPositionRecord
type EvidenceConflictEventRecord = knowledgecontract.EvidenceConflictEventRecord
type EvidenceConflictCaseRecord = knowledgecontract.EvidenceConflictCaseRecord
type EvidenceConflictListInput = knowledgecontract.EvidenceConflictListInput
type EvidenceConflictListResult = knowledgecontract.EvidenceConflictListResult
type EvidenceConflictCursor = knowledgecontract.EvidenceConflictCursor
type EvidenceConflictGetInput = knowledgecontract.EvidenceConflictGetInput
type EvidenceConflictEventCursor = knowledgecontract.EvidenceConflictEventCursor
type EvidenceConflictGetResult = knowledgecontract.EvidenceConflictGetResult
type EvidenceConflictResolutionInput = knowledgecontract.EvidenceConflictResolutionInput

func EncodeEvidenceConflictCursor(cursor EvidenceConflictCursor) (string, error) {
	return knowledgecontract.EncodeEvidenceConflictCursor(cursor)
}

func DecodeEvidenceConflictCursor(raw string) (*EvidenceConflictCursor, error) {
	return knowledgecontract.DecodeEvidenceConflictCursor(raw)
}

func EncodeEvidenceConflictEventCursor(cursor EvidenceConflictEventCursor) (string, error) {
	return knowledgecontract.EncodeEvidenceConflictEventCursor(cursor)
}

func DecodeEvidenceConflictEventCursor(raw string) (*EvidenceConflictEventCursor, error) {
	return knowledgecontract.DecodeEvidenceConflictEventCursor(raw)
}

type EvidenceConflictRepository interface {
	ListEvidenceConflicts(context.Context, EvidenceConflictListInput) (*EvidenceConflictListResult, error)
	GetEvidenceConflict(context.Context, EvidenceConflictGetInput) (*EvidenceConflictGetResult, error)
	ResolveEvidenceConflict(context.Context, EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error)
}

var _ EvidenceConflictRepository = (*LedgerRepositoryImpl)(nil)

// These small value helpers remain for legacy in-package fixtures. Their
// canonical persistence and validation paths are owned by knowledge/postgres.
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
	return sha256LengthDelimited(citation.CanonicalEvidenceID, citation.ContentHash, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end))
}

func evidenceConflictCaseKey(teamID, spaceID string, generation int64, positionKeys []string) string {
	keys := append([]string(nil), positionKeys...)
	sort.Strings(keys)
	parts := append([]string{teamID, spaceID, fmt.Sprintf("%d", generation)}, keys...)
	return sha256LengthDelimited(parts...)
}

func sha256LengthDelimited(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		var length [8]byte
		for index := range length {
			length[7-index] = byte(len(part) >> (index * 8))
		}
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
