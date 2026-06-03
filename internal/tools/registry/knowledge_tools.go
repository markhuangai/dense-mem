package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

// --- post_claim -----------------------------------------------------------

func postClaimTool(deps Dependencies) Tool {
	return Tool{
		Name:        "post_claim",
		Description: "Extract and persist a new Claim from supporting SourceFragments within the caller's profile. Returns the created claim with its initial candidate status.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"supported_by"},
			"properties": map[string]any{
				"supported_by": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "format": "uuid"},
					"minItems":    1,
					"description": "Fragment IDs that support this claim.",
				},
				"subject":         schemaString("Claim subject.", 256),
				"predicate":       schemaString("Claim predicate.", 128),
				"object":          schemaString("Claim object.", 1024),
				"modality":        schemaEnum([]string{"assertion", "question", "proposal", "speculation", "quoted"}),
				"polarity":        schemaEnum([]string{"+", "-"}),
				"speaker":         schemaString("Speaker of the claim.", 256),
				"extract_conf":    map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Extraction confidence [0,1]."},
				"resolution_conf": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Entity resolution confidence [0,1]."},
				"idempotency_key": schemaString("Client-supplied dedupe key (scoped to profile).", 128),
				"valid_from":      map[string]any{"type": "string", "format": "date-time"},
				"valid_to":        map[string]any{"type": "string", "format": "date-time"},
			},
			"additionalProperties": false,
		},
		OutputSchema:   claimObjectSchema(),
		RequiredScopes: []string{"write"},
		Invoke:         postClaimInvoker(deps.ClaimCreate),
	}
}

func postClaimInvoker(svc claimservice.CreateClaimService) ToolInvoker {
	return func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
		if svc == nil {
			return nil, ErrToolUnavailable
		}
		var claim domain.Claim
		if err := remapInput(input, &claim); err != nil {
			return nil, fmt.Errorf("post_claim: invalid input: %w", err)
		}
		res, err := svc.Create(ctx, profileID, &claim)
		if err != nil {
			return nil, err
		}
		out, err := structToMap(res.Claim)
		if err != nil {
			return nil, err
		}
		if res.Duplicate {
			out["duplicate"] = true
			out["duplicate_of"] = res.DuplicateOf
		}
		return out, nil
	}
}

// --- get_claim -----------------------------------------------------------

func getClaimTool(deps Dependencies) Tool {
	return Tool{
		Name:        "get_claim",
		Description: "Fetch a single Claim by ID within the caller's profile scope.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"id"},
			"properties":           map[string]any{"id": schemaString("Claim ID.", 128)},
			"additionalProperties": false,
		},
		OutputSchema:   claimObjectSchema(),
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.ClaimGet == nil {
				return nil, ErrToolUnavailable
			}
			id, _ := input["id"].(string)
			if id == "" {
				return nil, errors.New("get_claim: id is required")
			}
			claim, err := deps.ClaimGet.Get(ctx, profileID, id)
			if err != nil {
				return nil, err
			}
			return structToMap(claim)
		},
	}
}

// --- list_claims ----------------------------------------------------------

func listClaimsTool(deps Dependencies) Tool {
	return Tool{
		Name:        "list_claims",
		Description: "List claims for the caller's profile with optional status/modality filters and offset pagination.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":  map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
				"cursor": schemaString("Pagination cursor from a previous response.", 256),
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items":       map[string]any{"type": "array", "items": claimObjectSchema()},
				"next_cursor": map[string]any{"type": "string"},
				"has_more":    map[string]any{"type": "boolean"},
				"total":       map[string]any{"type": "integer"},
			},
		},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.ClaimList == nil {
				return nil, ErrToolUnavailable
			}
			limit := 20
			if v, ok := input["limit"].(float64); ok {
				limit = int(v)
			}
			claims, total, err := deps.ClaimList.List(ctx, profileID, limit, 0)
			if err != nil {
				return nil, err
			}
			items := make([]map[string]any, 0, len(claims))
			for _, c := range claims {
				m, err := structToMap(c)
				if err != nil {
					return nil, err
				}
				items = append(items, m)
			}
			return map[string]any{
				"items":       items,
				"next_cursor": "",
				"has_more":    false,
				"total":       total,
			}, nil
		},
	}
}

// --- verify_claim ---------------------------------------------------------

func verifyClaimTool(deps Dependencies) Tool {
	return Tool{
		Name:        "verify_claim",
		Description: "Run entailment verification for a Claim within the caller's profile. Transitions status from candidate to validated or rejected.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"id"},
			"properties":           map[string]any{"id": schemaString("Claim ID to verify.", 128)},
			"additionalProperties": false,
		},
		OutputSchema:   claimObjectSchema(),
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.ClaimVerify == nil {
				return nil, ErrToolUnavailable
			}
			id, _ := input["id"].(string)
			if id == "" {
				return nil, errors.New("verify_claim: id is required")
			}
			claim, err := deps.ClaimVerify.Verify(ctx, profileID, id)
			if err != nil {
				return nil, err
			}
			return structToMap(claim)
		},
	}
}

// --- promote_claim --------------------------------------------------------

func promoteClaimTool(deps Dependencies) Tool {
	return Tool{
		Name:        "promote_claim",
		Description: "Promote a validated Claim to a Fact within the caller's profile. The Claim must have status=validated and verdict=entailed.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"claim_id"},
			"properties":           map[string]any{"claim_id": schemaString("ID of the validated Claim to promote.", 128)},
			"additionalProperties": false,
		},
		OutputSchema:   factObjectSchema(),
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.FactPromote == nil {
				return nil, ErrToolUnavailable
			}
			claimID, _ := input["claim_id"].(string)
			if claimID == "" {
				return nil, errors.New("promote_claim: claim_id is required")
			}
			fact, err := deps.FactPromote.Promote(ctx, profileID, claimID)
			if err != nil {
				return nil, err
			}
			return structToMap(fact)
		},
	}
}

// --- get_fact -------------------------------------------------------------

func getFactTool(deps Dependencies) Tool {
	return Tool{
		Name:        "get_fact",
		Description: "Fetch a single Fact by ID within the caller's profile scope.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"id"},
			"properties": map[string]any{
				"id":               schemaString("Fact ID.", 128),
				"valid_at":         map[string]any{"type": "string", "format": "date-time"},
				"known_at":         map[string]any{"type": "string", "format": "date-time"},
				"include_evidence": map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
		OutputSchema:   factObjectSchema(),
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.FactGet == nil {
				return nil, ErrToolUnavailable
			}
			var req struct {
				ID              string     `json:"id"`
				ValidAt         *time.Time `json:"valid_at"`
				KnownAt         *time.Time `json:"known_at"`
				IncludeEvidence bool       `json:"include_evidence"`
			}
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("get_fact: invalid input: %w", err)
			}
			if req.ID == "" {
				return nil, errors.New("get_fact: id is required")
			}
			fact, err := deps.FactGet.Get(ctx, profileID, req.ID)
			if err != nil {
				return nil, err
			}
			if !factMatchesTemporalWindow(fact, req.ValidAt, req.KnownAt) {
				return nil, factservice.ErrFactNotFound
			}
			if !req.IncludeEvidence {
				factCopy := *fact
				factCopy.Evidence = nil
				fact = &factCopy
			}
			return structToMap(fact)
		},
	}
}

// --- list_facts -----------------------------------------------------------

func listFactsTool(deps Dependencies) Tool {
	return Tool{
		Name:        "list_facts",
		Description: "List facts for the caller's profile with optional filters and keyset pagination.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":            map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
				"cursor":           schemaString("Pagination cursor from a previous response.", 256),
				"subject":          schemaString("Filter by subject.", 256),
				"predicate":        schemaString("Filter by predicate.", 128),
				"status":           schemaEnum([]string{"active", "retracted", "superseded", "needs_revalidation"}),
				"valid_at":         map[string]any{"type": "string", "format": "date-time"},
				"known_at":         map[string]any{"type": "string", "format": "date-time"},
				"include_evidence": map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items":       map[string]any{"type": "array", "items": factObjectSchema()},
				"next_cursor": map[string]any{"type": "string"},
				"has_more":    map[string]any{"type": "boolean"},
			},
		},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.FactList == nil {
				return nil, ErrToolUnavailable
			}
			var req struct {
				Limit           int        `json:"limit"`
				Cursor          string     `json:"cursor"`
				Subject         string     `json:"subject"`
				Predicate       string     `json:"predicate"`
				Status          string     `json:"status"`
				ValidAt         *time.Time `json:"valid_at"`
				KnownAt         *time.Time `json:"known_at"`
				IncludeEvidence bool       `json:"include_evidence"`
			}
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("list_facts: invalid input: %w", err)
			}
			limit := req.Limit
			if limit == 0 {
				limit = 20
			}
			filters := factservice.FactListFilters{
				Subject:   req.Subject,
				Predicate: req.Predicate,
				Status:    domain.FactStatus(req.Status),
				ValidAt:   req.ValidAt,
				KnownAt:   req.KnownAt,
			}
			facts, nextCursor, err := deps.FactList.List(ctx, profileID, filters, limit, req.Cursor)
			if err != nil {
				return nil, err
			}
			items := make([]map[string]any, 0, len(facts))
			for _, f := range facts {
				if !req.IncludeEvidence {
					factCopy := *f
					factCopy.Evidence = nil
					f = &factCopy
				}
				m, err := structToMap(f)
				if err != nil {
					return nil, err
				}
				items = append(items, m)
			}
			return map[string]any{
				"items":       items,
				"next_cursor": nextCursor,
				"has_more":    nextCursor != "",
			}, nil
		},
	}
}

// --- retract_fact ---------------------------------------------------------

func retractFactTool(deps Dependencies) Tool {
	return Tool{
		Name:        "retract_fact",
		Description: "Withdraw a Fact from current knowledge within the caller's profile. The fact is soft-tombstoned; graph lineage and validity timestamps are preserved.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"id"},
			"properties":           map[string]any{"id": schemaString("Fact ID to retract.", 128)},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"status": schemaEnum([]string{"retracted"})},
		},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.FactRetract == nil {
				return nil, ErrToolUnavailable
			}
			id, _ := input["id"].(string)
			if id == "" {
				return nil, errors.New("retract_fact: id is required")
			}
			if err := deps.FactRetract.Retract(ctx, profileID, id); err != nil {
				return nil, err
			}
			return map[string]any{"status": "retracted"}, nil
		},
	}
}

// --- retract_fragment -----------------------------------------------------

func retractFragmentTool(deps Dependencies) Tool {
	return Tool{
		Name:        "retract_fragment",
		Description: "Tombstone a SourceFragment and recompute affected facts within the caller's profile. The fragment is soft-deleted; graph lineage is preserved.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"id"},
			"properties":           map[string]any{"id": schemaString("Fragment ID to retract.", 128)},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.FragmentRetract == nil {
				return nil, ErrToolUnavailable
			}
			id, _ := input["id"].(string)
			if id == "" {
				return nil, errors.New("retract_fragment: id is required")
			}
			if err := deps.FragmentRetract.Retract(ctx, profileID, id); err != nil {
				return nil, err
			}
			return map[string]any{}, nil
		},
	}
}

// --- detect_community -----------------------------------------------------

func detectCommunityTool(deps Dependencies) Tool {
	return Tool{
		Name:        "detect_community",
		Description: "Run graph community detection for the caller's profile using the Neo4j Graph Data Science plugin, persist deterministic summaries, and return the current community set.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"gamma":      map[string]any{"type": "number", "minimum": 0, "description": "Leiden resolution parameter. Defaults to 1.0."},
				"tolerance":  map[string]any{"type": "number", "minimum": 0, "description": "Convergence threshold for iterative algorithms."},
				"max_levels": map[string]any{"type": "integer", "minimum": 1, "description": "Maximum hierarchical merge levels."},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": communityDetectObjectSchema(),
		},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.CommunityDetect == nil || deps.CommunityList == nil {
				return nil, ErrToolUnavailable
			}
			var req dto.CommunityDetectRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("detect_community: invalid input: %w", err)
			}
			if err := validation.ValidateStruct(&req); err != nil {
				return nil, fmt.Errorf("detect_community: validation: %w", err)
			}
			opts := communityservice.DetectOptions{
				Gamma:     req.Gamma,
				Tolerance: req.Tolerance,
				MaxLevels: req.MaxLevels,
			}
			if err := deps.CommunityDetect.Detect(ctx, profileID, opts); err != nil {
				return nil, err
			}
			communities, err := deps.CommunityList.List(ctx, profileID, 0)
			if err != nil {
				return nil, err
			}
			items := make([]map[string]any, 0, len(communities))
			nodeCount := 0
			for _, community := range communities {
				m, err := structToMap(community)
				if err != nil {
					return nil, err
				}
				items = append(items, m)
				nodeCount += community.MemberCount
			}
			return map[string]any{
				"detected":        true,
				"community_count": len(items),
				"node_count":      nodeCount,
				"communities":     items,
			}, nil
		},
	}
}

// --- get_community_summary ------------------------------------------------

func getCommunitySummaryTool(deps Dependencies) Tool {
	return Tool{
		Name:        "get_community_summary",
		Description: "Fetch one persisted community summary by community_id within the caller's profile scope.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"community_id"},
			"properties": map[string]any{
				"community_id": schemaString("Community identifier.", 128),
			},
			"additionalProperties": false,
		},
		OutputSchema:   communityObjectSchema(),
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.CommunityGet == nil {
				return nil, ErrToolUnavailable
			}
			communityID, _ := input["community_id"].(string)
			if communityID == "" {
				return nil, errors.New("get_community_summary: community_id is required")
			}
			community, err := deps.CommunityGet.Get(ctx, profileID, communityID)
			if err != nil {
				return nil, err
			}
			return structToMap(community)
		},
	}
}

// --- list_communities -----------------------------------------------------

func listCommunitiesTool(deps Dependencies) Tool {
	return Tool{
		Name:        "list_communities",
		Description: "List persisted community summaries for the caller's profile, ordered by member_count descending.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{"type": "array", "items": communityObjectSchema()},
				"total": map[string]any{"type": "integer"},
			},
		},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.CommunityList == nil {
				return nil, ErrToolUnavailable
			}
			limit := 20
			if v, ok := input["limit"].(float64); ok {
				limit = int(v)
			}
			communities, err := deps.CommunityList.List(ctx, profileID, limit)
			if err != nil {
				return nil, err
			}
			items := make([]map[string]any, 0, len(communities))
			for _, community := range communities {
				m, err := structToMap(community)
				if err != nil {
					return nil, err
				}
				items = append(items, m)
			}
			return map[string]any{
				"items": items,
				"total": len(items),
			}, nil
		},
	}
}

func factMatchesTemporalWindow(f *domain.Fact, validAt, knownAt *time.Time) bool {
	if f == nil {
		return false
	}
	if validAt != nil {
		if f.ValidFrom != nil && f.ValidFrom.After(*validAt) {
			return false
		}
		if f.ValidTo != nil && !f.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if f.RecordedAt.After(*knownAt) {
			return false
		}
		if f.RecordedTo != nil && !f.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

// --- schema helpers -------------------------------------------------------

// claimObjectSchema mirrors dto.ClaimResponse. Hand-built to avoid reflection.
func claimObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"claim_id":           map[string]any{"type": "string"},
			"team_id":            map[string]any{"type": "string"},
			"subject":            map[string]any{"type": "string"},
			"predicate":          map[string]any{"type": "string"},
			"object":             map[string]any{"type": "string"},
			"modality":           map[string]any{"type": "string"},
			"polarity":           map[string]any{"type": "string"},
			"speaker":            map[string]any{"type": "string"},
			"span_start":         map[string]any{"type": "integer"},
			"span_end":           map[string]any{"type": "integer"},
			"valid_from":         map[string]any{"type": "string", "format": "date-time"},
			"valid_to":           map[string]any{"type": "string", "format": "date-time"},
			"recorded_at":        map[string]any{"type": "string", "format": "date-time"},
			"recorded_to":        map[string]any{"type": "string", "format": "date-time"},
			"extract_conf":       map[string]any{"type": "number"},
			"resolution_conf":    map[string]any{"type": "number"},
			"source_quality":     map[string]any{"type": "number"},
			"entailment_verdict": map[string]any{"type": "string"},
			"status":             map[string]any{"type": "string"},
			"extraction_model":   map[string]any{"type": "string"},
			"extraction_version": map[string]any{"type": "string"},
			"verifier_model":     map[string]any{"type": "string"},
			"pipeline_run_id":    map[string]any{"type": "string"},
			"content_hash":       map[string]any{"type": "string"},
			"idempotency_key":    map[string]any{"type": "string"},
			"classification":     map[string]any{"type": "object"},
			"supported_by":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"evidence":           map[string]any{"type": "array", "items": evidenceObjectSchema()},
		},
	}
}

// factObjectSchema mirrors dto.FactResponse. Hand-built to avoid reflection.
func factObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fact_id":                        map[string]any{"type": "string"},
			"team_id":                        map[string]any{"type": "string"},
			"subject":                        map[string]any{"type": "string"},
			"predicate":                      map[string]any{"type": "string"},
			"object":                         map[string]any{"type": "string"},
			"status":                         map[string]any{"type": "string"},
			"truth_score":                    map[string]any{"type": "number"},
			"valid_from":                     map[string]any{"type": "string", "format": "date-time"},
			"valid_to":                       map[string]any{"type": "string", "format": "date-time"},
			"recorded_at":                    map[string]any{"type": "string", "format": "date-time"},
			"recorded_to":                    map[string]any{"type": "string", "format": "date-time"},
			"retracted_at":                   map[string]any{"type": "string", "format": "date-time"},
			"last_confirmed_at":              map[string]any{"type": "string", "format": "date-time"},
			"promoted_from_claim_id":         map[string]any{"type": "string"},
			"classification":                 map[string]any{"type": "object"},
			"classification_lattice_version": map[string]any{"type": "string"},
			"source_quality":                 map[string]any{"type": "number"},
			"labels":                         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"metadata":                       map[string]any{"type": "object"},
			"evidence":                       map[string]any{"type": "array", "items": evidenceObjectSchema()},
		},
	}
}

func communityObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"community_id":       map[string]any{"type": "string"},
			"team_id":            map[string]any{"type": "string"},
			"level":              map[string]any{"type": "integer"},
			"summary":            map[string]any{"type": "string"},
			"summary_version":    map[string]any{"type": "string"},
			"member_count":       map[string]any{"type": "integer"},
			"top_entities":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"top_predicates":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"last_summarized_at": map[string]any{"type": "string", "format": "date-time"},
		},
	}
}

func communityDetectObjectSchema() map[string]any {
	return map[string]any{
		"detected":        map[string]any{"type": "boolean"},
		"community_count": map[string]any{"type": "integer"},
		"node_count":      map[string]any{"type": "integer"},
		"communities":     map[string]any{"type": "array", "items": communityObjectSchema()},
	}
}
