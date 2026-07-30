import { RecallHit, RecallPayload } from "./api";

export function recallPayloadForHits(hits: RecallHit[]): RecallPayload {
  return {
    recall_id: "recall-test",
    results: hits.map((hit, index) => {
      const evidence = evidenceForHit(hit);
      return {
        ...evidence,
        relationship_ids: relationshipsForHit(hit).map((relationship) => relationship.relationship_id),
        rank: hit.semantic_rank ?? index + 1,
      };
    }),
    conflicts: [],
    related_communities: hits.map((hit) => ({
      evidence_ids: [evidenceForHit(hit).evidence_id],
      relationships: relationshipsForHit(hit),
    })),
    related_relationships: [],
    related_hypotheses: [],
    search_states: { evidence: "current", relationships: "current" },
    degradations: [],
  };
}

function evidenceForHit(hit: RecallHit) {
	if (!hit.evidence) {
		throw new Error("test recall hit must include evidence");
	}
	return hit.evidence;
}

function relationshipsForHit(hit: RecallHit) {
  if (hit.relationships?.length) {
    return hit.relationships;
  }
  if (hit.relationship) {
    return [hit.relationship];
  }
	return [];
}
