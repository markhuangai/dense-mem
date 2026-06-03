# Dense-Mem UAT — Knowledge Pipeline

Playwright end-to-end tests for the dense-mem knowledge pipeline API.

## UAT Coverage Map

| File | UATs | Phase |
|------|------|-------|
| `phase1-indexes.spec.ts` | UAT-01a–e, AC-X2 regression | Neo4j schema bootstrap |
| `phase2-claim-create.spec.ts` | UAT-02a–e | Claim creation & profile isolation |
| `phase2-claim-dedupe.spec.ts` | UAT-03a–b | Claim deduplication |
| `phase3-verify.spec.ts` | UAT-04a–d | Claim verification happy path |
| `phase3-verify-errors.spec.ts` | UAT-05a–c, AC-X6 regression | Verify error taxonomy |
| `phase4-promote.spec.ts` | UAT-06a–f | Promote to fact, list & get facts |
| `phase4-contradiction.spec.ts` | UAT-07a–d | Contradiction detection on promote |
| `phase4-promote-concurrency.spec.ts` | UAT-08a–c | Concurrent promote safety |
| `phase6-retract.spec.ts` | UAT-09a–e | Fragment retraction & cascade |
| `phase7-community.spec.ts` | UAT-10a–c | Community detection |
| `phase8-mcp.spec.ts` | UAT-11a-d | MCP server tool surface |
| `phase9-recall.spec.ts` | UAT-12a-e, AC-X2 regression | Semantic recall endpoint |
| `profile-key-permissions.spec.ts` | Permissions regression | Read-only profile key HTTP/MCP enforcement |
| `control-plane-lifecycle.spec.ts` | Admin regression | Team profile key lifecycle, expiry, rate limit, audit |
| `auth-matrix.spec.ts` | Auth regression | Missing/invalid auth and read-only write denial matrix |
| `cli-workflows.spec.ts` | Operator regression | Container CLI provision/list/rotate/delete workflow |
| `control-portal-live.spec.ts` | Portal regression | Real browser portal flow against live backend |
| `e2e-journey.spec.ts` | UAT-13, AC-X2, AC-X6, isolation | Full pipeline, trace, and context end-to-end |

## Prerequisites

1. A running dense-mem server (default `http://localhost:8080`)
2. Neo4j reachable (default `bolt://localhost:7687`)
3. Valid API key; cross-profile tests need a second API key

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `BASE_URL` | Server base URL | `http://localhost:8080` |
| `API_KEY` | Standard API key | `test-api-key` |
| `API_KEY_B` | Secondary API key for cross-profile isolation checks | unset |
| `PROFILE_ID` | Default profile ID for tests | `test-profile-id` |
| `TEAM_ID` | Team ID for team-profile management tests; falls back to `PROFILE_ID` | unset |
| `CONTROL_BASE_URL` | Control portal URL for live portal UAT | `http://127.0.0.1:8090` |
| `CONTROL_PORTAL_TOKEN` | Control portal token for live portal UAT | unset |
| `NEO4J_URI` | Neo4j Bolt URI | `bolt://localhost:7687` |
| `NEO4J_USER` | Neo4j username | `neo4j` |
| `NEO4J_PASSWORD` | Neo4j password | `password` |

## Running Tests

```bash
# Install dependencies
cd tests/uat
npm install
npx playwright install

# List all tests without running them
npx playwright test --list

# Run all tests against a live server
BASE_URL=http://localhost:8080 API_KEY=<key> API_KEY_B=<second-key> PROFILE_ID=<team-id> \
  npx playwright test

# Run a single phase
npx playwright test phase2-claim-create.spec.ts

# Run profile key permission checks
TEAM_ID=<team-id> API_KEY=<read-write-key> npx playwright test profile-key-permissions.spec.ts

# Run admin/control-plane checks
TEAM_ID=<team-id> API_KEY=<read-write-key> npm run test:control-plane

# Run container CLI workflow checks after docker compose is up
npm run test:cli

# Run real control portal browser checks
CONTROL_PORTAL_TOKEN=<token> npm run test:portal-live

# Run all admin/control-plane gaps
TEAM_ID=<team-id> API_KEY=<read-write-key> CONTROL_PORTAL_TOKEN=<token> npm run test:admin

# Run with verbose output
npx playwright test --reporter=list
```

## Red/Green Status

The Playwright specs are live UAT coverage, not red scaffolding. Tests that prove
cross-profile isolation use `API_KEY_B`; when it is absent, those specific checks
are skipped instead of faking isolation with a profile string.

## Helper Utilities (`helpers.ts`)

| Export | Purpose |
|--------|---------|
| `headers(profileId)` | Standard auth headers; profile scope is derived from `API_KEY` |
| `headersForApiKey(apiKey)` | Auth headers for a specific API key |
| `neo4jQuery(cypher, params)` | Direct Neo4j query for assertions |
| `spawnMcp(env)` | Call the `/mcp` Streamable HTTP endpoint |
| `seedFragmentForProfile(request, profileId, content, opts)` | Create a fragment via API |
| `seedFragmentWithHeaders(request, headers, content, opts)` | Create a fragment with explicit auth headers |
| `createAndVerifyClaim(request, profileId, opts)` | Create + verify a claim |
| `createAndPromoteClaim(request, profileId, opts)` | Create + verify + promote a claim |
| `createTwoSupportPromotedFact(request, profileId, subject)` | Promote two facts for same subject |
| `createValidatedCandidateForVerify(request, profileId, opts)` | Create a validated claim |

## Route Surface Under Test

```
GET  /api/v1/recall
POST /api/v1/claims
GET  /api/v1/claims/:id
POST /api/v1/claims/:id/verify
POST /api/v1/claims/:id/promote
GET  /api/v1/facts/:id
GET  /api/v1/facts
POST /api/v1/fragments
POST /api/v1/fragments/:id/retract
POST /api/v1/tools/detect_community
POST /api/v1/tools/trace_memory
POST /api/v1/tools/assemble_context
GET  /api/v1/teams/:teamId
PATCH /api/v1/teams/:teamId
GET  /api/v1/teams/:teamId/audit-log
GET  /api/v1/teams/:teamId/profiles
POST /api/v1/teams/:teamId/profiles
GET  /api/v1/teams/:teamId/profiles/:profileId
POST /api/v1/teams/:teamId/profiles/:profileId/rotate
DELETE /api/v1/teams/:teamId/profiles/:profileId
POST /mcp
GET  /api/v1/openapi.json
```
