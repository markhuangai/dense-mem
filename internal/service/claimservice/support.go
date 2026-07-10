package claimservice

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/classification"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// supportedFragmentsReader is the minimal Neo4j interface required by
// loadSupportingFragments. It is satisfied by the ProfileScopeEnforcer
// returned by neo4j.NewProfileScopeEnforcer — callers inject that concrete
// value so no additional wiring is required.
type supportedFragmentsReader interface {
	ScopedRead(
		ctx context.Context,
		profileID string,
		query string,
		params map[string]any,
	) (neo4j.ResultSummary, []map[string]any, error)
}

// supportResult aggregates data loaded from SourceFragment nodes. It is
// consumed by both the claim-creation and claim-verification flows.
type supportResult struct {
	// Fragments holds one domain.Fragment per requested fragment ID, in
	// Neo4j result order.
	Fragments []*domain.Fragment

	// MaxSourceQuality is the highest source_quality value across all
	// returned fragments. It is used to set the quality signal on the claim.
	MaxSourceQuality float64

	// MergedClassification is the dimension-wise lattice maximum of all
	// fragment classification maps, computed via classification.DefaultLattice().
	// Unknown dimensions are passed through with last-write-wins semantics.
	MergedClassification map[string]string
}

// loadSupportingFragmentsQuery fetches active SourceFragment nodes by ID,
// scoped to $profileId. The coalesce guard treats a missing status as 'active'
// so that legacy nodes (written before the status field existed) are still
// returned. Quarantined and retracted nodes are excluded.
//
// Profile isolation: $profileId is injected automatically by ScopedRead;
// callers MUST NOT include profileId in the params map.
const loadSupportingFragmentsQuery = `MATCH (sf:SourceFragment {team_id: $profileId})
WHERE sf.fragment_id IN $fragmentIds
  AND coalesce(sf.status, 'active') = 'active'
RETURN sf.fragment_id  AS fragment_id,
       sf.content       AS content,
       sf.source_quality AS source_quality,
       sf.classification AS classification,
       sf.classification_json AS classification_json,
       sf.authority AS authority`

// loadSupportingFragments fetches one or more SourceFragment nodes by ID,
// scoped to profileID. It returns ErrSupportingFragmentMissing when any
// requested fragment is absent from the result (either because it does not
// exist, belongs to a different profile, or is not active).
//
// Classification maps from all fragments are merged via the default
// classification lattice so callers receive a single consolidated view.
func loadSupportingFragments(
	ctx context.Context,
	reader supportedFragmentsReader,
	profileID string,
	fragmentIDs []string,
) (*supportResult, error) {
	if len(fragmentIDs) == 0 {
		return &supportResult{MergedClassification: map[string]string{}}, nil
	}

	_, rows, err := reader.ScopedRead(ctx, profileID, loadSupportingFragmentsQuery, map[string]any{
		"fragmentIds": fragmentIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("loadSupportingFragments: %w", err)
	}

	lattice := classification.DefaultLattice()
	merged := map[string]string{}

	found := make(map[string]struct{}, len(fragmentIDs))
	fragments := make([]*domain.Fragment, 0, len(rows))
	var maxSQ float64

	for _, row := range rows {
		fid, _ := row["fragment_id"].(string)
		content, _ := row["content"].(string)
		sq, _ := row["source_quality"].(float64)
		classRaw := fragmentcodec.DecodeOptionalMap(row["classification"])
		if classRaw == nil {
			classRaw = fragmentcodec.DecodeOptionalMap(row["classification_json"])
		}
		authority, _ := row["authority"].(string)

		found[fid] = struct{}{}

		// Convert map[string]any to map[string]string for the lattice.
		// Values that are not strings are skipped; they carry no lattice rank.
		classStr := make(map[string]string, len(classRaw))
		for k, v := range classRaw {
			if sv, ok := v.(string); ok {
				classStr[k] = sv
			}
		}
		merged = lattice.Max(merged, classStr)

		fragments = append(fragments, &domain.Fragment{
			FragmentID:     fid,
			ProfileID:      profileID,
			Content:        content,
			SourceQuality:  sq,
			Classification: classRaw,
			Authority:      domain.Authority(authority),
		})

		if sq > maxSQ {
			maxSQ = sq
		}
	}

	// Every requested fragment must have been found. An absent entry means
	// the fragment does not exist in this profile, or was retracted. Either
	// way, the caller cannot proceed with incomplete evidence.
	for _, id := range fragmentIDs {
		if _, ok := found[id]; !ok {
			return nil, fmt.Errorf("%w: fragment_id=%s team_id=%s",
				ErrSupportingFragmentMissing, id, profileID)
		}
	}

	return &supportResult{
		Fragments:            fragments,
		MaxSourceQuality:     maxSQ,
		MergedClassification: merged,
	}, nil
}
