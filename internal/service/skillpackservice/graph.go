package skillpackservice

import (
	"context"
	"fmt"
	"strings"
	"time"

	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// GraphStore is the Neo4j profile-scoped surface used for import tagging and
// rollback. Production wiring passes storage/neo4j.ProfileScopeEnforcer.
type GraphStore interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4jdriver.ResultSummary, []map[string]any, error)
	ScopedWrite(ctx context.Context, profileID string, query string, params map[string]any) (neo4jdriver.ResultSummary, error)
	ScopedWriteTx(ctx context.Context, profileID string, fn func(tx neo4jdriver.ManagedTransaction) error) error
}

type graphOps struct {
	graph GraphStore
}

func newGraphOps(graph GraphStore) *graphOps {
	return &graphOps{graph: graph}
}

func (g *graphOps) available() bool {
	return g != nil && g.graph != nil
}

func (g *graphOps) findCandidates(ctx context.Context, profileID, query string, limit int) ([]Candidate, error) {
	if !g.available() {
		return nil, nil
	}
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil, nil
	}
	predicates := []string{"has_skill", "knows", "uses"}
	factParams := map[string]any{
		"terms":      terms,
		"predicates": predicates,
		"limit":      int64(limit),
	}
	_, factRows, err := g.graph.ScopedRead(ctx, profileID, `
		MATCH (f:Fact {team_id: $profileId})
		WHERE f.status = 'active'
		  AND f.predicate IN $predicates
		  AND all(term IN $terms WHERE toLower(coalesce(f.subject, '') + ' ' + coalesce(f.predicate, '') + ' ' + coalesce(f.object, '')) CONTAINS term)
		RETURN f.fact_id AS id,
		       'fact' AS type,
		       f.subject AS subject,
		       f.predicate AS predicate,
		       f.object AS object,
		       f.recorded_at AS recorded_at
		ORDER BY f.recorded_at DESC
		LIMIT $limit
	`, factParams)
	if err != nil {
		return nil, err
	}

	out := make([]Candidate, 0, len(factRows))
	for _, row := range factRows {
		out = append(out, candidateFromRow(row, SourceKindFact))
	}
	if len(out) >= limit {
		return out, nil
	}

	claimParams := map[string]any{
		"terms":      terms,
		"predicates": predicates,
		"limit":      int64(limit - len(out)),
	}
	_, claimRows, err := g.graph.ScopedRead(ctx, profileID, `
		MATCH (c:Claim {team_id: $profileId})
		WHERE c.status = 'validated'
		  AND c.predicate IN $predicates
		  AND all(term IN $terms WHERE toLower(coalesce(c.subject, '') + ' ' + coalesce(c.predicate, '') + ' ' + coalesce(c.object, '')) CONTAINS term)
		RETURN c.claim_id AS id,
		       'claim' AS type,
		       c.subject AS subject,
		       c.predicate AS predicate,
		       c.object AS object,
		       c.recorded_at AS recorded_at
		ORDER BY c.recorded_at DESC
		LIMIT $limit
	`, claimParams)
	if err != nil {
		return nil, err
	}
	for _, row := range claimRows {
		out = append(out, candidateFromRow(row, SourceKindValidatedClaim))
	}
	return out, nil
}

func candidateFromRow(row map[string]any, sourceKind string) Candidate {
	recordedAt, _ := row["recorded_at"].(time.Time)
	return Candidate{
		ID:   stringState(row, "id"),
		Type: stringState(row, "type"),
		Item: SkillPackItem{
			Subject:    stringState(row, "subject"),
			Predicate:  stringState(row, "predicate"),
			Object:     stringState(row, "object"),
			SourceKind: sourceKind,
		},
		Snippet:    stringState(row, "object"),
		RecordedAt: recordedAt,
	}
}

func (g *graphOps) tagFragment(ctx context.Context, profileID, fragmentID, importID, artifactHash string) error {
	if !g.available() {
		return nil
	}
	_, err := g.graph.ScopedWrite(ctx, profileID, `
		MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $fragmentId})
		SET sf.import_id = $importId,
		    sf.import_bundle_hash = $artifactHash,
		    sf.imported = true
	`, map[string]any{
		"fragmentId":   fragmentID,
		"importId":     importID,
		"artifactHash": artifactHash,
	})
	return err
}

func (g *graphOps) tagClaim(ctx context.Context, profileID, claimID, importID, artifactHash, sourceKind string) error {
	if !g.available() {
		return nil
	}
	_, err := g.graph.ScopedWrite(ctx, profileID, `
		MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
		SET c.import_id = $importId,
		    c.import_bundle_hash = $artifactHash,
		    c.import_source_kind = $sourceKind,
		    c.imported = true
	`, map[string]any{
		"claimId":      claimID,
		"importId":     importID,
		"artifactHash": artifactHash,
		"sourceKind":   sourceKind,
	})
	return err
}

func (g *graphOps) trustClaim(ctx context.Context, profileID, claimID, importID, artifactHash, sourceKind string) error {
	if !g.available() {
		return fmt.Errorf("skill pack import: graph store is required for trusted import")
	}
	now := time.Now().UTC()
	_, err := g.graph.ScopedWrite(ctx, profileID, `
		MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
		SET c.status = 'validated',
		    c.entailment_verdict = 'entailed',
		    c.verifier_model = 'skill_pack.source_trust',
		    c.verified_at = $verifiedAt,
		    c.import_id = $importId,
		    c.import_bundle_hash = $artifactHash,
		    c.import_source_kind = $sourceKind,
		    c.imported = true
	`, map[string]any{
		"claimId":      claimID,
		"verifiedAt":   now,
		"importId":     importID,
		"artifactHash": artifactHash,
		"sourceKind":   sourceKind,
	})
	return err
}

func (g *graphOps) trustExistingClaim(ctx context.Context, profileID, claimID string) error {
	if !g.available() {
		return fmt.Errorf("skill pack import: graph store is required for trusted import")
	}
	now := time.Now().UTC()
	_, err := g.graph.ScopedWrite(ctx, profileID, `
		MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
		SET c.status = 'validated',
		    c.entailment_verdict = 'entailed',
		    c.verifier_model = 'skill_pack.source_trust',
		    c.verified_at = $verifiedAt
	`, map[string]any{
		"claimId":    claimID,
		"verifiedAt": now,
	})
	return err
}

func (g *graphOps) tagFact(ctx context.Context, profileID, factID, importID, artifactHash, sourceKind string) error {
	if !g.available() {
		return nil
	}
	_, err := g.graph.ScopedWrite(ctx, profileID, `
		MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
		SET f.import_id = $importId,
		    f.import_bundle_hash = $artifactHash,
		    f.import_source_kind = $sourceKind,
		    f.imported = true
	`, map[string]any{
		"factId":       factID,
		"importId":     importID,
		"artifactHash": artifactHash,
		"sourceKind":   sourceKind,
	})
	return err
}

func (g *graphOps) supersedeFacts(ctx context.Context, profileID string, factIDs []string, claimID, importID string) error {
	if len(factIDs) == 0 {
		return nil
	}
	if !g.available() {
		return fmt.Errorf("skill pack import: graph store is required to supersede facts")
	}
	now := time.Now().UTC()
	return g.graph.ScopedWriteTx(ctx, profileID, func(tx neo4jdriver.ManagedTransaction) error {
		result, err := neo4jstorage.RunScoped(ctx, tx, profileID, `
			UNWIND $factIds AS factId
			MATCH (f:Fact {team_id: $profileId, fact_id: factId})
			SET f.status = 'superseded',
			    f.recorded_to = $now,
			    f.valid_to = $now,
			    f.import_superseded_by_import_id = $importId
			WITH f
			MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
			MERGE (f)-[r:SUPERSEDED_BY {team_id: $profileId, import_id: $importId}]->(c)
		`, map[string]any{
			"factIds":  factIDs,
			"claimId":  claimID,
			"importId": importID,
			"now":      now,
		})
		if err != nil {
			return err
		}
		_, err = result.Consume(ctx)
		return err
	})
}

func (g *graphOps) deleteEntity(ctx context.Context, profileID, entityType, entityID string) error {
	if !g.available() {
		return fmt.Errorf("skill pack rollback: graph store is required")
	}
	query, params, err := deleteEntityQuery(entityType, entityID)
	if err != nil {
		return err
	}
	_, err = g.graph.ScopedWrite(ctx, profileID, query, params)
	return err
}

func deleteEntityQuery(entityType, entityID string) (string, map[string]any, error) {
	switch entityType {
	case "fragment":
		return `
			MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $entityId})
			DETACH DELETE sf
		`, map[string]any{"entityId": entityID}, nil
	case "claim":
		return `
			MATCH (c:Claim {team_id: $profileId, claim_id: $entityId})
			DETACH DELETE c
		`, map[string]any{"entityId": entityID}, nil
	case "fact":
		return `
			MATCH (f:Fact {team_id: $profileId, fact_id: $entityId})
			DETACH DELETE f
		`, map[string]any{"entityId": entityID}, nil
	default:
		return "", nil, fmt.Errorf("unsupported entity_type %q", entityType)
	}
}

func (g *graphOps) restoreClaim(ctx context.Context, profileID, claimID, importID string, before map[string]any) error {
	if !g.available() {
		return fmt.Errorf("skill pack rollback: graph store is required")
	}
	_, err := g.graph.ScopedWrite(ctx, profileID, `
		MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
			SET c.status = $status,
			    c.entailment_verdict = $entailmentVerdict,
			    c.verifier_model = $verifierModel,
			    c.verified_at = $verifiedAt,
			    c.last_verifier_response = $lastVerifierResponse
		`, map[string]any{
		"claimId":              claimID,
		"status":               stringState(before, "status"),
		"entailmentVerdict":    stringState(before, "entailment_verdict"),
		"verifierModel":        nullableStringState(before, "verifier_model"),
		"verifiedAt":           nullableTimeState(before, "verified_at"),
		"lastVerifierResponse": nullableStringState(before, "last_verifier_response"),
	})
	return err
}

func (g *graphOps) restoreFact(ctx context.Context, profileID, factID, importID string, before map[string]any) error {
	if !g.available() {
		return fmt.Errorf("skill pack rollback: graph store is required")
	}
	return g.graph.ScopedWriteTx(ctx, profileID, func(tx neo4jdriver.ManagedTransaction) error {
		result, err := neo4jstorage.RunScoped(ctx, tx, profileID, `
			MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
			SET f.status = $status,
			    f.recorded_to = $recordedTo,
			    f.valid_to = $validTo,
			    f.retracted_at = $retractedAt,
			    f.last_confirmed_at = $lastConfirmedAt
			REMOVE f.import_superseded_by_import_id
			WITH f
			OPTIONAL MATCH (f)-[r:SUPERSEDED_BY {team_id: $profileId, import_id: $importId}]->()
			DELETE r
		`, map[string]any{
			"factId":          factID,
			"importId":        importID,
			"status":          stringState(before, "status"),
			"recordedTo":      nullableTimeState(before, "recorded_to"),
			"validTo":         nullableTimeState(before, "valid_to"),
			"retractedAt":     nullableTimeState(before, "retracted_at"),
			"lastConfirmedAt": nullableTimeState(before, "last_confirmed_at"),
		})
		if err != nil {
			return err
		}
		_, err = result.Consume(ctx)
		return err
	})
}

func (g *graphOps) currentState(ctx context.Context, profileID, entityType, entityID string) (map[string]any, error) {
	if !g.available() {
		return nil, fmt.Errorf("skill pack rollback: graph store is required")
	}
	query, params, err := currentStateQuery(entityType, entityID)
	if err != nil {
		return nil, err
	}
	_, rows, err := g.graph.ScopedRead(ctx, profileID, query, params)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	props, _ := rows[0]["state"].(map[string]any)
	return props, nil
}

func currentStateQuery(entityType, entityID string) (string, map[string]any, error) {
	switch entityType {
	case "fragment":
		return `
			MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $entityId})
			RETURN {
				fragment_id: sf.fragment_id,
				content_hash: sf.content_hash,
				import_id: sf.import_id
			} AS state
		`, map[string]any{"entityId": entityID}, nil
	case "claim":
		return `
			MATCH (c:Claim {team_id: $profileId, claim_id: $entityId})
			RETURN {
				claim_id: c.claim_id,
				subject: c.subject,
				predicate: c.predicate,
				object: c.object,
				status: c.status,
				entailment_verdict: c.entailment_verdict,
				verifier_model: c.verifier_model,
				verified_at: c.verified_at,
				last_verifier_response: c.last_verifier_response,
				import_id: c.import_id
			} AS state
		`, map[string]any{"entityId": entityID}, nil
	case "fact":
		return `
			MATCH (f:Fact {team_id: $profileId, fact_id: $entityId})
			RETURN {
				fact_id: f.fact_id,
				subject: f.subject,
				predicate: f.predicate,
				object: f.object,
				status: f.status,
				import_id: f.import_id,
				import_superseded_by_import_id: f.import_superseded_by_import_id
			} AS state
		`, map[string]any{"entityId": entityID}, nil
	default:
		return "", nil, fmt.Errorf("unsupported entity_type %q", entityType)
	}
}

func stringState(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func nullableStringState(m map[string]any, key string) any {
	v := stringState(m, key)
	if v == "" {
		return nil
	}
	return v
}

func nullableTimeState(m map[string]any, key string) any {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case time.Time:
		return v
	case string:
		if v == "" {
			return nil
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return nil
		}
		return t
	default:
		return nil
	}
}
