import { RecallHit, RecallPayload } from "./api";

export function recallPayloadForHits(hits: RecallHit[]): RecallPayload {
  return {
    recall_id: "recall-test",
    results: hits.map((hit, index) => {
      const evidence = evidenceForHit(hit, index);
      return {
        ...evidence,
        relationship_ids: relationshipsForHit(hit, index).map((relationship) => relationship.relationship_id),
        rank: hit.semantic_rank ?? index + 1,
      };
    }),
    conflicts: [],
    related_communities: hits.map((hit, index) => ({
      evidence_ids: [evidenceForHit(hit, index).evidence_id],
      relationships: relationshipsForHit(hit, index),
    })),
    related_relationships: [],
    related_hypotheses: [],
    search_states: { evidence: "current", relationships: "current" },
    degradations: [],
  };
}

function evidenceForHit(hit: RecallHit, index: number) {
  if (hit.evidence) {
    return hit.evidence;
  }
  if (hit.fact) {
    return {
      evidence_id: `evidence-${hit.fact.fact_id}`,
      context: `${hit.fact.subject} ${hit.fact.predicate}: ${hit.fact.object}`,
      source: "knowledge graph",
      created_at: hit.fact.recorded_at,
    };
  }
  if (hit.claim) {
    return {
      evidence_id: `evidence-${hit.claim.claim_id}`,
      context: `${hit.claim.subject} ${hit.claim.predicate}: ${hit.claim.object}`,
      source: "knowledge graph",
      created_at: hit.claim.recorded_at,
    };
  }
  const fragment = hit.fragment;
  return {
    evidence_id: fragment ? `evidence-${fragment.fragment_id}` : `evidence-${index + 1}`,
    context: fragment?.content ?? "",
    source: fragment?.source || fragment?.source_type || "evidence",
    source_type: fragment?.source_type,
    created_at: fragment?.created_at,
  };
}

function relationshipsForHit(hit: RecallHit, index: number) {
  if (hit.relationships?.length) {
    return hit.relationships;
  }
  if (hit.relationship) {
    return [hit.relationship];
  }
  const source = hit.fact ?? hit.claim;
  if (!source) {
    return [];
  }
  return [{
    relationship_id: "fact_id" in source ? source.fact_id : source.claim_id,
    subject: {
      entity_id: "entity-alice",
      name: source.subject,
      kind: "person",
    },
    predicate: source.predicate,
    object: {
      entity_id: `entity-object-${index + 1}`,
      name: source.object,
      kind: "concept",
    },
    tier: hit.tier,
    polarity: "polarity" in source ? source.polarity : "+",
    evidence_ids: [evidenceForHit(hit, index).evidence_id],
  }];
}
