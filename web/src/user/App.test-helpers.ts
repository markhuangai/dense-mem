import { screen } from "@testing-library/react";
import { expect } from "vitest";
import { RecallHit, RecallPayload } from "./api";

export function optionValue<T>(value: T | T[], index: number): T {
  return Array.isArray(value) ? value[Math.min(index, value.length - 1)] : value;
}

export function isRecallSequence(value: RecallHit[] | RecallHit[][]): value is RecallHit[][] {
  return Array.isArray(value[0]);
}

export function authorizationHeader(init?: RequestInit): string {
  const headers = init?.headers;
  if (!headers) {
    return "";
  }
  if (headers instanceof Headers) {
    return headers.get("Authorization") ?? "";
  }
  if (Array.isArray(headers)) {
    return headers.find(([name]) => name.toLowerCase() === "authorization")?.[1] ?? "";
  }
  return (headers as Record<string, string>).Authorization ?? (headers as Record<string, string>).authorization ?? "";
}

export function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export async function expectCurrentWorkspace(teamName: string) {
  const workspace = await screen.findByLabelText("Current workspace");
  expect(workspace).toHaveTextContent(teamName);
}

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
    related_communities: [],
    discovery_paths: hits.map((hit) => {
      const evidenceID = evidenceForHit(hit).evidence_id;
      return {
        community_id: `community-${evidenceID}`,
        logical_community_id: `logical-community-${evidenceID}`,
        rank: 1,
        summary: "test community",
        top_entities: [],
        top_predicates: [],
        entity_count: 0,
        relationship_count: relationshipsForHit(hit).length,
        relationships: relationshipsForHit(hit).map((relationship) => ({
          ...relationship,
          evidence_ids: relationship.evidence_ids?.length ? relationship.evidence_ids : [evidenceID],
        })),
        relationships_truncated: false,
      };
    }),
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
