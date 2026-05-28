/**
 * UAT-01 — Phase 1: Neo4j schema indexes and constraints exist.
 *
 * Verifies that the server bootstraps the required Neo4j schema:
 * - SourceFragment vector index (`fragment_embedding_idx`)
 * - Fact recall full-text index (`fact_recall_idx`)
 * - Uniqueness constraints on SourceFragment and Fact nodes
 *
 * These tests exercise the schema bootstrapper directly via Neo4j and
 * validate that the authenticated OpenAPI surface still exposes the
 * expected runtime routes.
 */

import { test, expect } from '@playwright/test';
import { headers, neo4jQuery, BASE_URL } from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase1';

// UAT-01a: Vector index on SourceFragment exists in Neo4j
test('UAT-01a: SourceFragment vector index exists', async () => {
  const rows = await neo4jQuery(
    "SHOW VECTOR INDEXES YIELD name WHERE name = 'fragment_embedding_idx' RETURN name",
  );
  expect(rows.length).toBeGreaterThan(0);
});

// UAT-01b: Full-text recall index on Fact exists in Neo4j
test('UAT-01b: Fact recall full-text index exists', async () => {
  const rows = await neo4jQuery(
    "SHOW INDEXES YIELD name, type WHERE name = 'fact_recall_idx' AND type = 'FULLTEXT' RETURN name",
  );
  expect(rows.length).toBeGreaterThan(0);
});

// UAT-01c: Uniqueness constraint on SourceFragment.id exists
test('UAT-01c: SourceFragment uniqueness constraint exists', async () => {
  const rows = await neo4jQuery(
    "SHOW CONSTRAINTS YIELD name, labelsOrTypes " +
      "WHERE 'SourceFragment' IN labelsOrTypes RETURN name",
  );
  expect(rows.length).toBeGreaterThan(0);
});

// UAT-01d: Uniqueness constraint on Claim.id exists
test('UAT-01d: Claim uniqueness constraint exists', async () => {
  const rows = await neo4jQuery(
    "SHOW CONSTRAINTS YIELD name, labelsOrTypes " +
      "WHERE 'Claim' IN labelsOrTypes RETURN name",
  );
  expect(rows.length).toBeGreaterThan(0);
});

// UAT-01e: Uniqueness constraint on Fact.id exists
test('UAT-01e: Fact uniqueness constraint exists', async () => {
  const rows = await neo4jQuery(
    "SHOW CONSTRAINTS YIELD name, labelsOrTypes " +
      "WHERE 'Fact' IN labelsOrTypes RETURN name",
  );
  expect(rows.length).toBeGreaterThan(0);
});

// AC-X2 regression: OpenAPI schema enumerates claim and fact routes
test('AC-X2 regression: OpenAPI lists /claims and /facts routes', async ({ request }) => {
  const res = await request.get(`${BASE_URL}/api/v1/openapi.json`, {
    headers: headers(profileId),
  });
  expect(res.status()).toBe(200);
  const spec = await res.json();
  expect(spec).toHaveProperty('paths');
  const paths: Record<string, unknown> = spec.paths;
  expect(Object.keys(paths)).toEqual(
    expect.arrayContaining([
      expect.stringMatching('/claims'),
      expect.stringMatching('/facts'),
    ]),
  );
});
