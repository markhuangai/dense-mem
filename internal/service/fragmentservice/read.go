package fragmentservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	"github.com/markhuangai/dense-mem/internal/service/graphrow"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

// ErrFragmentNotFound is returned when a fragment does not exist in the requested profile scope.
// This is mapped to HTTP 404 — the same response is used for both missing fragments and
// cross-profile reads so existence is not leaked across profiles (AC-27).
var ErrFragmentNotFound = errors.New("fragment not found")

// ScopedReader is the local interface for profile-scoped reads.
// This mirrors neo4j.ScopedReader to avoid import cycles.
type ScopedReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (any, []map[string]any, error)
}

// GetFragmentService retrieves a single fragment by ID within a profile scope.
type GetFragmentService interface {
	// GetByID returns the fragment with the given ID in the given profile.
	// Returns ErrFragmentNotFound if the fragment does not exist OR if it
	// belongs to a different profile (the two cases are indistinguishable
	// to the caller by design).
	GetByID(ctx context.Context, profileID, fragmentID string) (*domain.Fragment, error)
}

// BatchGetFragmentService retrieves multiple fragments in one profile-scoped read.
type BatchGetFragmentService interface {
	GetByIDs(ctx context.Context, profileID string, fragmentIDs []string) (map[string]*domain.Fragment, error)
}

// getFragmentService implements GetFragmentService via Neo4j.
type getFragmentService struct {
	reader ScopedReader
}

var _ GetFragmentService = (*getFragmentService)(nil)
var _ BatchGetFragmentService = (*getFragmentService)(nil)

// NewGetFragmentService constructs a GetFragmentService.
func NewGetFragmentService(reader ScopedReader) GetFragmentService {
	return &getFragmentService{reader: reader}
}

// GetByID executes a profile-scoped read and maps the result to a domain.Fragment.
func (s *getFragmentService) GetByID(ctx context.Context, profileID, fragmentID string) (*domain.Fragment, error) {
	query := `
		MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $fragmentId})
		WHERE ` + neo4j.FragmentActiveFilter + `
		RETURN sf.fragment_id AS fragment_id,
		       sf.team_id AS team_id,
		       sf.content AS content,
		       sf.source AS source,
		       sf.source_type AS source_type,
		       sf.authority AS authority,
			       sf.labels AS labels,
			       sf.metadata AS metadata,
			       sf.metadata_json AS metadata_json,
			       sf.recall_text AS recall_text,
			       sf.identifier_tokens AS identifier_tokens,
			       sf.content_hash AS content_hash,
		       sf.idempotency_key AS idempotency_key,
		       sf.embedding_model AS embedding_model,
		       sf.embedding_dimensions AS embedding_dimensions,
		       sf.source_quality AS source_quality,
		       sf.classification AS classification,
		       sf.classification_json AS classification_json,
		       sf.recorded_to AS recorded_to,
		       sf.retracted_at AS retracted_at,
		       sf.owner_profile_id AS owner_profile_id,
		       sf.owner_profile_name AS owner_profile_name,
		       sf.created_by_profile_id AS created_by_profile_id,
		       sf.created_by_profile_name AS created_by_profile_name,
		       sf.created_at AS created_at,
		       sf.updated_at AS updated_at
		LIMIT 1
	`
	params := map[string]any{
		"fragmentId": fragmentID,
	}

	_, results, err := s.reader.ScopedRead(ctx, profileID, query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to read fragment: %w", err)
	}

	if len(results) == 0 {
		return nil, ErrFragmentNotFound
	}

	return mapRowToFragment(results[0]), nil
}

// GetByIDs executes one profile-scoped read for the requested fragment IDs.
// Missing IDs are omitted from the returned map so callers can preserve their
// existing miss-handling behavior.
func (s *getFragmentService) GetByIDs(ctx context.Context, profileID string, fragmentIDs []string) (map[string]*domain.Fragment, error) {
	out := make(map[string]*domain.Fragment, len(fragmentIDs))
	if len(fragmentIDs) == 0 {
		return out, nil
	}

	query := `
		MATCH (sf:SourceFragment {team_id: $profileId})
		WHERE sf.fragment_id IN $fragmentIds AND ` + neo4j.FragmentActiveFilter + `
		RETURN sf.fragment_id AS fragment_id,
		       sf.team_id AS team_id,
		       sf.content AS content,
		       sf.source AS source,
		       sf.source_type AS source_type,
		       sf.authority AS authority,
			       sf.labels AS labels,
			       sf.metadata AS metadata,
			       sf.metadata_json AS metadata_json,
			       sf.recall_text AS recall_text,
			       sf.identifier_tokens AS identifier_tokens,
			       sf.content_hash AS content_hash,
		       sf.idempotency_key AS idempotency_key,
		       sf.embedding_model AS embedding_model,
		       sf.embedding_dimensions AS embedding_dimensions,
		       sf.source_quality AS source_quality,
		       sf.classification AS classification,
		       sf.classification_json AS classification_json,
		       sf.recorded_to AS recorded_to,
		       sf.retracted_at AS retracted_at,
		       sf.owner_profile_id AS owner_profile_id,
		       sf.owner_profile_name AS owner_profile_name,
		       sf.created_by_profile_id AS created_by_profile_id,
		       sf.created_by_profile_name AS created_by_profile_name,
		       sf.created_at AS created_at,
		       sf.updated_at AS updated_at
	`

	_, results, err := s.reader.ScopedRead(ctx, profileID, query, map[string]any{
		"fragmentIds": fragmentIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to batch read fragments: %w", err)
	}
	for _, row := range results {
		fragment := mapRowToFragment(row)
		if fragment.FragmentID != "" {
			out[fragment.FragmentID] = fragment
		}
	}
	return out, nil
}

// mapRowToFragment converts a Neo4j result map to a domain.Fragment.
// Uses neo4j.CoerceSourceType so legacy rows with missing source_type default to manual (AC-46).
func mapRowToFragment(row map[string]any) *domain.Fragment {
	f := &domain.Fragment{}

	f.FragmentID = graphrow.String(row, "fragment_id")
	f.ProfileID = graphrow.String(row, "team_id")
	f.OwnerProfileID = graphrow.String(row, "owner_profile_id")
	f.OwnerProfileName = graphrow.String(row, "owner_profile_name")
	f.CreatedByProfileID = graphrow.String(row, "created_by_profile_id")
	f.CreatedByProfileName = graphrow.String(row, "created_by_profile_name")
	if f.OwnerProfileID == "" {
		f.OwnerProfileID = f.CreatedByProfileID
	}
	if f.OwnerProfileName == "" {
		f.OwnerProfileName = f.CreatedByProfileName
	}
	f.Content = graphrow.String(row, "content")
	f.Source = graphrow.String(row, "source")

	f.SourceType = neo4j.CoerceSourceType(row["source_type"])
	if v := graphrow.String(row, "authority"); v != "" {
		f.Authority = domain.Authority(v)
	} else {
		f.Authority = domain.AuthorityUnknown
	}

	f.Labels = graphrow.StringSlice(row, "labels")
	if v := fragmentcodec.DecodeOptionalMap(row["metadata"]); v != nil {
		f.Metadata = v
	} else if v := fragmentcodec.DecodeOptionalMap(row["metadata_json"]); v != nil {
		f.Metadata = v
	}
	f.RecallText = graphrow.String(row, "recall_text")
	f.IdentifierTokens = graphrow.StringSlice(row, "identifier_tokens")
	f.ContentHash = graphrow.String(row, "content_hash")
	f.IdempotencyKey = graphrow.String(row, "idempotency_key")
	f.EmbeddingModel = graphrow.String(row, "embedding_model")
	f.EmbeddingDimensions = graphrow.Int(row, "embedding_dimensions")
	f.SourceQuality = graphrow.Float64(row, "source_quality")
	if v := fragmentcodec.DecodeOptionalMap(row["classification"]); v != nil {
		f.Classification = v
	} else if v := fragmentcodec.DecodeOptionalMap(row["classification_json"]); v != nil {
		f.Classification = v
	}
	f.RecordedTo = graphrow.TimePtr(row, "recorded_to")
	f.RetractedAt = graphrow.TimePtr(row, "retracted_at")
	f.CreatedAt = graphrow.Time(row, "created_at")
	f.UpdatedAt = graphrow.Time(row, "updated_at")

	return f
}
