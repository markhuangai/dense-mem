package openapi

// knowledgeSchemas returns component-schema definitions for REST resources that
// need explicit schema refs. These schemas are merged into every generated spec
// so routes can reference them via RouteDescriptor fields, decoupled from MCP
// tool registration.
//
// Schema names follow the PascalCase component convention used throughout this
// package. They are intentionally separate from tool-derived schemas (which
// use schemaNameFor) to avoid name collisions and to allow independent
// evolution of the REST surface vs. the tool surface.
func knowledgeSchemas() map[string]any {
	return map[string]any{
		// CreateFragmentRequest is the raw REST request body for creating a
		// source fragment. The server generates embeddings before persistence.
		"CreateFragmentRequest": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "Fragment text.",
					"maxLength":   8192,
				},
				"source_type": map[string]any{
					"type": "string",
					"enum": []string{"conversation", "document", "observation", "manual"},
				},
				"source": map[string]any{
					"type":      "string",
					"maxLength": 256,
				},
				"authority": map[string]any{
					"type": "string",
					"enum": []string{"authoritative", "primary", "secondary", "inferred", "unknown"},
				},
				"idempotency_key": map[string]any{
					"type":      "string",
					"maxLength": 128,
				},
				"labels": map[string]any{
					"type":     "array",
					"items":    map[string]any{"type": "string", "maxLength": 64},
					"maxItems": 20,
				},
				"metadata": map[string]any{
					"type":                 "object",
					"additionalProperties": true,
				},
				"source_quality": map[string]any{
					"type":    "number",
					"format":  "float",
					"minimum": 0,
					"maximum": 1,
				},
				"classification": map[string]any{
					"type":                 "object",
					"additionalProperties": true,
				},
			},
			"required": []string{"content"},
		},

		// FragmentResponse mirrors internal/http/dto.FragmentResponse and
		// intentionally excludes the embedding vector.
		"FragmentResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"fragment_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"team_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"owner_profile_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"owner_profile_name": map[string]any{
					"type": "string",
				},
				"created_by_profile_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"created_by_profile_name": map[string]any{
					"type": "string",
				},
				"content": map[string]any{
					"type": "string",
				},
				"source_type": map[string]any{
					"type": "string",
				},
				"source": map[string]any{
					"type": "string",
				},
				"authority": map[string]any{
					"type": "string",
				},
				"labels": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"metadata": map[string]any{
					"type":                 "object",
					"additionalProperties": true,
				},
				"content_hash": map[string]any{
					"type": "string",
				},
				"idempotency_key": map[string]any{
					"type": "string",
				},
				"embedding_model": map[string]any{
					"type": "string",
				},
				"embedding_dimensions": map[string]any{
					"type": "integer",
				},
				"source_quality": map[string]any{
					"type":   "number",
					"format": "float",
				},
				"classification": map[string]any{
					"type":                 "object",
					"additionalProperties": true,
				},
				"status": map[string]any{
					"type": "string",
				},
				"recorded_to": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
				"retracted_at": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
				"created_at": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
				"updated_at": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
			},
			"required": []string{"id", "fragment_id", "content", "source_type", "content_hash", "embedding_model", "embedding_dimensions", "source_quality", "created_at", "updated_at"},
		},

		// ClaimRequest is the request body for creating a candidate claim from
		// one or more supporting fragments.
		"ClaimRequest": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"supported_by": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string", "format": "uuid"},
					"minItems":    1,
					"description": "Source fragment IDs that support this claim.",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "Subject of the claim triple.",
					"maxLength":   256,
				},
				"predicate": map[string]any{
					"type":        "string",
					"description": "Predicate of the claim triple.",
					"maxLength":   128,
				},
				"object": map[string]any{
					"type":        "string",
					"description": "Object of the claim triple.",
					"maxLength":   1024,
				},
				"modality": map[string]any{
					"type":        "string",
					"enum":        []string{"assertion", "question", "proposal", "speculation", "quoted"},
					"description": "Epistemic modality of the claim.",
				},
				"polarity": map[string]any{
					"type":        "string",
					"enum":        []string{"+", "-"},
					"description": "Affirmative or negating polarity.",
				},
				"speaker": map[string]any{
					"type":        "string",
					"description": "Speaker or source attribution for the claim.",
					"maxLength":   256,
				},
				"extract_conf": map[string]any{
					"type":        "number",
					"format":      "float",
					"minimum":     0,
					"maximum":     1,
					"description": "Extraction confidence score (0-1).",
				},
				"resolution_conf": map[string]any{
					"type":        "number",
					"format":      "float",
					"minimum":     0,
					"maximum":     1,
					"description": "Entity-resolution confidence score (0-1).",
				},
				"idempotency_key": map[string]any{
					"type":        "string",
					"description": "Client-supplied idempotency key scoped to the team.",
					"maxLength":   128,
				},
				"valid_from": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
				"valid_to": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
			},
			"required": []string{"supported_by"},
		},

		// ClaimResponse is the body returned for a single claim resource.
		"ClaimResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"claim_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"team_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"created_by_profile_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"created_by_profile_name": map[string]any{
					"type": "string",
				},
				"subject": map[string]any{
					"type": "string",
				},
				"predicate": map[string]any{
					"type": "string",
				},
				"object": map[string]any{
					"type": "string",
				},
				"modality": map[string]any{
					"type": "string",
					"enum": []string{"assertion", "question", "proposal", "speculation", "quoted"},
				},
				"polarity": map[string]any{
					"type": "string",
					"enum": []string{"+", "-"},
				},
				"speaker": map[string]any{
					"type": "string",
				},
				"span_start": map[string]any{
					"type": "integer",
				},
				"span_end": map[string]any{
					"type": "integer",
				},
				"valid_from": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
				"valid_to": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
				"recorded_at": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
				"recorded_to": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
				"extract_conf": map[string]any{
					"type":   "number",
					"format": "float",
				},
				"resolution_conf": map[string]any{
					"type":   "number",
					"format": "float",
				},
				"source_quality": map[string]any{
					"type":   "number",
					"format": "float",
				},
				"entailment_verdict": map[string]any{
					"type": "string",
					"enum": []string{"entailed", "contradicted", "neutral", "insufficient"},
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"candidate", "validated", "rejected", "superseded", "disputed", "promoted"},
					"description": "Lifecycle state of the claim.",
				},
				"extraction_model": map[string]any{
					"type": "string",
				},
				"extraction_version": map[string]any{
					"type": "string",
				},
				"verifier_model": map[string]any{
					"type": "string",
				},
				"pipeline_run_id": map[string]any{
					"type": "string",
				},
				"content_hash": map[string]any{
					"type": "string",
				},
				"idempotency_key": map[string]any{
					"type": "string",
				},
				"classification": map[string]any{
					"type": "object",
				},
				"supported_by": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string", "format": "uuid"},
				},
				"evidence": map[string]any{
					"type":  "array",
					"items": knowledgeEvidenceSchema(),
				},
			},
			"required": []string{"claim_id", "team_id", "subject", "predicate", "object", "modality", "polarity", "span_start", "span_end", "recorded_at", "extract_conf", "resolution_conf", "source_quality", "entailment_verdict", "status", "extraction_model", "content_hash"},
		},

		// FactResponse is the body returned for a promoted, validated fact node.
		// Field names mirror the FactResponse DTO in internal/http/dto/fact.go.
		// Facts are immutable once created; only the status field may be updated
		// (e.g. to "retracted").
		"FactResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"fact_id": map[string]any{
					"type":        "string",
					"format":      "uuid",
					"description": "Unique identifier of the promoted fact.",
				},
				"team_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"created_by_profile_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"created_by_profile_name": map[string]any{
					"type": "string",
				},
				"promoted_by_profile_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"promoted_by_profile_name": map[string]any{
					"type": "string",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "Subject of the subject-predicate-object triple.",
				},
				"predicate": map[string]any{
					"type":        "string",
					"description": "Predicate of the subject-predicate-object triple.",
				},
				"object": map[string]any{
					"type":        "string",
					"description": "Object of the subject-predicate-object triple.",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"active", "retracted", "superseded", "needs_revalidation"},
					"description": "Lifecycle state of the fact.",
				},
				"truth_score": map[string]any{
					"type":        "number",
					"format":      "float",
					"minimum":     0,
					"maximum":     1,
					"description": "Confidence score assigned during promotion (0–1).",
				},
				"valid_from": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Optional start of the fact's validity window.",
				},
				"valid_to": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Optional end of the fact's validity window.",
				},
				"recorded_at": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Timestamp when the fact was recorded in the graph.",
				},
				"recorded_to": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Timestamp when the fact stopped being current in transaction time.",
				},
				"retracted_at": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Timestamp when the fact was retracted, if applicable.",
				},
				"last_confirmed_at": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Timestamp of the most recent confirmation of this fact.",
				},
				"promoted_from_claim_id": map[string]any{
					"type":        "string",
					"format":      "uuid",
					"description": "The validated claim this fact was promoted from.",
				},
				"classification": map[string]any{
					"type":        "object",
					"description": "Structured classification labels applied at promotion time.",
				},
				"classification_lattice_version": map[string]any{
					"type":        "string",
					"description": "Version of the classification lattice used.",
				},
				"source_quality": map[string]any{
					"type":        "number",
					"format":      "float",
					"minimum":     0,
					"maximum":     1,
					"description": "Quality score of the originating source fragment (0–1).",
				},
				"labels": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Free-form labels attached to the fact.",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Arbitrary key-value metadata attached at promotion time.",
				},
				"evidence": map[string]any{
					"type":        "array",
					"items":       knowledgeEvidenceSchema(),
					"description": "Supporting evidence lineage for the fact.",
				},
			},
			"required": []string{"fact_id", "team_id", "subject", "predicate", "object", "status", "truth_score", "recorded_at", "promoted_from_claim_id", "source_quality"},
		},

		// FactRequest is the request body for promoting a validated claim to a fact.
		// The claim referenced must already be in the "validated" lifecycle state.
		"FactRequest": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"claim_id": map[string]any{
					"type":        "string",
					"format":      "uuid",
					"description": "Validated claim to promote to a fact.",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Arbitrary key-value metadata attached at promotion time.",
				},
			},
			"required": []string{"claim_id"},
		},

		// VerifyClaimResponse is the body returned after an entailment verification
		// run. It captures the verdict, updated status, and verifier metadata.
		"VerifyClaimResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"claim_id": map[string]any{
					"type":   "string",
					"format": "uuid",
				},
				"entailment_verdict": map[string]any{
					"type":        "string",
					"enum":        []string{"entailed", "contradicted", "insufficient"},
					"description": "Outcome of the entailment check.",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"candidate", "validated", "rejected", "superseded", "disputed", "promoted"},
					"description": "Updated lifecycle state of the claim after verification.",
				},
				"last_verifier_response": map[string]any{
					"type":        "string",
					"description": "Raw reasoning text returned by the verifier model.",
				},
				"verifier_model": map[string]any{
					"type":        "string",
					"description": "Model identifier used for verification.",
				},
				"verified_at": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
			},
			"required": []string{"claim_id", "entailment_verdict", "status"},
		},

		// RetractFragmentResponse is the body returned after a fragment is
		// soft-tombstoned via POST /api/v1/fragments/:id/retract. The fragment
		// node is preserved in the graph for lineage but excluded from all
		// active-fragment reads (AC-48).
		"RetractFragmentResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"retracted"},
					"description": "Confirmation that the fragment has been soft-tombstoned.",
				},
			},
			"required": []string{"status"},
		},

		"RetractFactResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"retracted"},
					"description": "Confirmation that the fact has been soft-tombstoned.",
				},
			},
			"required": []string{"status"},
		},

		// CommunityDetectRequest is the request body for
		// Community detection request/response payloads.
		// All fields are optional tuning parameters for the Leiden projection.
		"CommunityDetectRequest": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"gamma": map[string]any{
					"type":        "number",
					"format":      "float",
					"minimum":     0,
					"description": "Resolution parameter for Leiden community detection. Higher values produce more, smaller communities.",
				},
				"tolerance": map[string]any{
					"type":        "number",
					"format":      "float",
					"minimum":     0,
					"description": "Convergence threshold for iterative algorithms. Smaller values increase precision.",
				},
				"max_levels": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "Maximum number of hierarchical community-merge levels.",
				},
			},
		},
		"CommunityDetectResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"detected": map[string]any{
					"type": "boolean",
				},
				"community_count": map[string]any{
					"type": "integer",
				},
				"node_count": map[string]any{
					"type": "integer",
				},
				"communities": map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/components/schemas/CommunityResponse"},
				},
			},
			"required": []string{"detected", "community_count", "node_count"},
		},

		// RecallHitResponse is one ranked result returned by GET /api/v1/recall.
		// Tier classifies the knowledge-pipeline level:
		//   "1"   = active Fact (highest authority)
		//   "1.5" = validated Claim
		//   "2"   = SourceFragment (raw evidence)
		"RecallHitResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tier": map[string]any{
					"type":        "string",
					"enum":        []string{"1", "1.5", "2"},
					"description": "Knowledge-pipeline tier of this hit: 1=active fact, 1.5=validated claim, 2=source fragment.",
				},
				"score": map[string]any{
					"type":        "number",
					"format":      "float",
					"description": "Normalised relevance score after tier weighting.",
				},
				"fragment": map[string]any{
					"$ref":        "#/components/schemas/FragmentResponse",
					"description": "Populated for tier-2 (SourceFragment) hits.",
				},
				"claim": map[string]any{
					"$ref":        "#/components/schemas/ClaimResponse",
					"description": "Populated for tier-1.5 (validated Claim) hits.",
				},
				"fact": map[string]any{
					"$ref":        "#/components/schemas/FactResponse",
					"description": "Populated for tier-1 (active Fact) hits.",
				},
				"semantic_rank": map[string]any{
					"type":        "integer",
					"description": "1-based rank from the semantic branch; 0 if absent.",
				},
				"keyword_rank": map[string]any{
					"type":        "integer",
					"description": "1-based rank from the keyword branch; 0 if absent.",
				},
				"final_score": map[string]any{
					"type":        "number",
					"format":      "float",
					"description": "Reciprocal Rank Fusion score (fragment hits only).",
				},
			},
		},

		// RecallResponse wraps the ranked list returned by GET /api/v1/recall.
		"RecallResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"data": map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/components/schemas/RecallHitResponse"},
				},
			},
			"required": []string{"data"},
		},

		// ToolCatalogEntry mirrors GET /api/v1/tools item payloads.
		"ToolCatalogEntry": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type": "string",
				},
				"description": map[string]any{
					"type": "string",
				},
				"input_schema": map[string]any{
					"type":                 "object",
					"additionalProperties": true,
				},
				"output_schema": map[string]any{
					"type":                 "object",
					"additionalProperties": true,
				},
				"required_scopes": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"contract_version": map[string]any{
					"type": "string",
				},
				"feature_gate": map[string]any{
					"type": "string",
				},
				"visibility": map[string]any{
					"type": "string",
				},
			},
			"required": []string{"name", "description", "input_schema", "output_schema", "required_scopes"},
		},

		// ToolCatalogResponse is the list envelope returned by GET /api/v1/tools.
		"ToolCatalogResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tools": map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/components/schemas/ToolCatalogEntry"},
				},
			},
			"required": []string{"tools"},
		},

		// ToolExecuteRequest is a permissive object because the concrete schema
		// varies by tool name and is discoverable from the tool catalog.
		"ToolExecuteRequest": map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"description":          "Tool-specific JSON arguments. Use GET /api/v1/tools to discover the exact input schema for a tool name.",
		},

		// ToolExecuteResponse is a permissive object because the concrete schema
		// varies by tool name and is discoverable from the tool catalog.
		"ToolExecuteResponse": map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"description":          "Tool-specific JSON response. Use GET /api/v1/tools to discover the exact output schema for a tool name.",
		},

		// CommunityResponse represents one persisted community summary.
		"CommunityResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"community_id": map[string]any{
					"type": "string",
				},
				"team_id": map[string]any{
					"type": "string",
				},
				"level": map[string]any{
					"type": "integer",
				},
				"summary": map[string]any{
					"type": "string",
				},
				"summary_version": map[string]any{
					"type": "string",
				},
				"member_count": map[string]any{
					"type": "integer",
				},
				"top_entities": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"top_predicates": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"last_summarized_at": map[string]any{
					"type":   "string",
					"format": "date-time",
				},
			},
			"required": []string{"community_id", "team_id", "level", "summary", "summary_version", "member_count", "last_summarized_at"},
		},
		"ListCommunitiesResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/components/schemas/CommunityResponse"},
				},
				"total": map[string]any{
					"type": "integer",
				},
			},
			"required": []string{"items", "total"},
		},
	}
}

func knowledgeEvidenceSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fragment_id":        map[string]any{"type": "string", "format": "uuid"},
			"speaker":            map[string]any{"type": "string"},
			"span_start":         map[string]any{"type": "integer"},
			"span_end":           map[string]any{"type": "integer"},
			"extract_conf":       map[string]any{"type": "number", "format": "float"},
			"extraction_model":   map[string]any{"type": "string"},
			"extraction_version": map[string]any{"type": "string"},
			"pipeline_run_id":    map[string]any{"type": "string"},
			"authority": map[string]any{
				"type": "string",
				"enum": []string{"authoritative", "primary", "secondary", "inferred", "unknown"},
			},
		},
	}
}
