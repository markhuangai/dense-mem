#!/usr/bin/env node

import { createHash, randomUUID } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamAID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const sharedCredentialID = requiredEnv("DENSE_MEM_E2E_CREDENTIAL_ID");
const sharedAPIKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const runID = `private-erasure-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
const irreversibleBody = { acknowledge_irreversible: true };
const privateSentinels = new Set();
const sensitiveValues = new Set([controlToken, sharedAPIKey]);
let rpcID = 0;
let tableLock = null;
let serverStopped = false;
let manifestTables;

try {
  await runScenario();
} finally {
  await releaseKnowledgeIngestLock();
  if (serverStopped) {
    startServer();
    await waitForServerReady();
  }
}

async function runScenario() {
  const teamC = await createTeam(`${runID} Team C`);
  const sso = await createSSOActors(teamC.id);
  await assertSSOSession(sso.a);
  await assertSSOSession(sso.b);
  await assertSSOSession(sso.c);

  const ownerA = await createControlCredential(teamAID, `${runID} owner A`, "credential_private");
  const ownerB = await createControlCredential(teamAID, `${runID} owner B`, "credential_private");
  const ownerC = await createControlCredential(teamC.id, `${runID} owner C`, "credential_private");
  const controlTarget = await createControlCredential(teamAID, `${runID} control`, "credential_private");
  const idempotencyTarget = await createControlCredential(teamAID, `${runID} idempotency target`, "credential_private");
  const recoveryTarget = await createControlCredential(teamAID, `${runID} recovery`, "credential_private");
  const retentionEligible = await createControlCredential(teamAID, `${runID} retention eligible`, "credential_private");
  const retentionHeld = await createControlCredential(teamAID, `${runID} retention held`, "credential_private");
  const ssoCredentialTarget = await createSSOCredential(sso.a, `${runID} SSO isolated`, "credential_private");
  const ssoProfileCredential = await createSSOCredential(sso.a, `${runID} SSO profile`, "profile_private");
  const ssoSharedCredential = await createSSOCredential(sso.a, `${runID} SSO shared`, "shared_only");

  for (const credential of [ownerA, ownerB, ownerC, controlTarget, idempotencyTarget, recoveryTarget, retentionEligible, retentionHeld, ssoCredentialTarget, ssoProfileCredential, ssoSharedCredential]) {
    sensitiveValues.add(credential.apiKey);
  }
  for (const actor of Object.values(sso)) {
    sensitiveValues.add(actor.sessionToken);
    sensitiveValues.add(actor.csrfToken);
  }

  const spaces = await loadSpaces();
  const sharedSpace = loadTeamSharedSpace(teamAID);
  const ownerASpace = credentialSpace(spaces, ownerA.credential.id);
  const ownerBSpace = credentialSpace(spaces, ownerB.credential.id);
  const ownerCSpace = credentialSpace(spaces, ownerC.credential.id);
  const controlSpace = credentialSpace(spaces, controlTarget.credential.id);
  const idempotencySpace = credentialSpace(spaces, idempotencyTarget.credential.id);
  const recoverySpace = credentialSpace(spaces, recoveryTarget.credential.id);
  const retentionEligibleSpace = credentialSpace(spaces, retentionEligible.credential.id);
  const retentionHeldSpace = credentialSpace(spaces, retentionHeld.credential.id);
  const ssoCredentialSpace = credentialSpace(spaces, ssoCredentialTarget.credential.id);
  const ssoProfileSpace = requiredSpace(spaces, (space) => space.team_id === teamAID && space.kind === "profile_private" && space.owner_profile_id === sso.a.identityID, "SSO profile-private");

  const sharedFixture = seedSpaceContent({ teamID: teamAID, ownerID: sharedCredentialID, keyID: sharedCredentialID, spaceID: sharedSpace.id, label: "team-shared", rich: true });
  const ownerAFixture = seedSpaceContent({ teamID: teamAID, ownerID: ownerA.credential.id, keyID: ownerA.credential.id, spaceID: ownerASpace.id, label: "owner-a", rich: true });
  const ownerBFixture = seedSpaceContent({ teamID: teamAID, ownerID: ownerB.credential.id, keyID: ownerB.credential.id, spaceID: ownerBSpace.id, label: "owner-b" });
  const ownerCFixture = seedSpaceContent({ teamID: teamC.id, ownerID: ownerC.credential.id, keyID: ownerC.credential.id, spaceID: ownerCSpace.id, label: "owner-c" });
  const controlFixture = seedSpaceContent({ teamID: teamAID, ownerID: controlTarget.credential.id, keyID: controlTarget.credential.id, spaceID: controlSpace.id, label: "control" });
  const recoveryFixture = seedSpaceContent({ teamID: teamAID, ownerID: recoveryTarget.credential.id, keyID: recoveryTarget.credential.id, spaceID: recoverySpace.id, label: "recovery" });
  const profileFixture = seedSpaceContent({ teamID: teamAID, ownerID: ssoProfileCredential.credential.id, keyID: ssoProfileCredential.credential.id, spaceID: ssoProfileSpace.id, label: "sso-profile" });
  const ssoCredentialFixture = seedSpaceContent({ teamID: teamAID, ownerID: ssoCredentialTarget.credential.id, keyID: ssoCredentialTarget.credential.id, spaceID: ssoCredentialSpace.id, label: "sso-credential", rich: true });
  const retentionEligibleFixture = seedSpaceContent({ teamID: teamAID, ownerID: retentionEligible.credential.id, keyID: retentionEligible.credential.id, spaceID: retentionEligibleSpace.id, label: "retention-eligible" });
  const retentionHeldFixture = seedSpaceContent({ teamID: teamAID, ownerID: retentionHeld.credential.id, keyID: retentionHeld.credential.id, spaceID: retentionHeldSpace.id, label: "retention-held" });

  await assertRichPrivateVisibility(ownerA, ownerB, ownerC, ownerAFixture, sharedFixture);

  await assertStrictErasureContract(ownerA.apiKey);
  await placeHold(ownerASpace.id, "owner_hold");
  await expectStatus(ownerRequest(ownerA.apiKey, "owner-held"), 409, "owner erasure bypassed legal hold");
  await releaseHold(ownerASpace.id);
  const ownerQueued = await ownerRequest(ownerA.apiKey, "owner-erase-1");
  assert(ownerQueued.status === 202, "credential owner erasure did not return 202");
  assertOwnerOperationShape(ownerQueued.payload.data);
  const ownerOperationID = requiredString(ownerQueued.payload.data?.operation_id, "owner operation ID");
  const ownerCompleted = await waitControlOperation(ownerOperationID, "completed");
  const ownerReplay = await ownerRequest(ownerA.apiKey, "owner-erase-1");
  assert(ownerReplay.status === 202 && ownerReplay.payload.data?.operation_id === ownerOperationID, "credential owner idempotency replay changed operation");
  await assertOwnerOperationVisibility(ownerOperationID, ownerA.apiKey, ownerB.apiKey, ownerC.apiKey);
  assertErasedSpace(ownerASpace.id, ownerCompleted, ownerAFixture, "active");
  await assertPrivateRecallAbsent(ownerA.apiKey, ownerAFixture.sentinel);
  await assertTraceAbsent(ownerA.apiKey, ownerAFixture.relationshipID);
  assertPreserved(ownerBSpace.id, ownerBFixture);
  assertPreserved(ownerCSpace.id, ownerCFixture);
  assertPreserved(sharedSpace.id, sharedFixture);

  assertWriteRejected(ownerASpace.id, teamAID, ownerA.credential.id, 1, "stale generation was accepted after erasure");
  const postEraseIngest = seedSingleIngest(teamAID, ownerA.credential.id, ownerASpace.id, `${runID}-post-erasure-current-generation`);
  privateSentinels.add(`${runID}-post-erasure-current-generation`);
  const ownerCleanup = await ownerRequest(ownerA.apiKey, "owner-erase-2");
  assert(ownerCleanup.status === 202, "credential owner cleanup erasure did not return 202");
  const ownerCleanupCompleted = await waitControlOperation(ownerCleanup.payload.data.operation_id, "completed");
  assertErasedSpace(ownerASpace.id, ownerCleanupCompleted, { ...ownerAFixture, ingestID: postEraseIngest }, "active");

  await placeHold(ssoProfileSpace.id, "profile_hold");
  await expectStatus(ssoRequest(sso.a, "/ui/api/sso/private-memory", { method: "DELETE", idempotencyKey: "profile-held", body: irreversibleBody }), 409, "SSO profile erasure bypassed legal hold");
  await releaseHold(ssoProfileSpace.id);
  const profileQueued = await ssoRequest(sso.a, "/ui/api/sso/private-memory", { method: "DELETE", idempotencyKey: "profile-erase", body: irreversibleBody });
  assert(profileQueued.status === 202, "SSO profile erasure did not return 202");
  assertOwnerOperationShape(profileQueued.payload.data);
  const profileOperationID = profileQueued.payload.data.operation_id;
  const profileCompleted = await waitControlOperation(profileOperationID, "completed");
  const profileReplay = await ssoRequest(sso.a, "/ui/api/sso/private-memory", { method: "DELETE", idempotencyKey: "profile-erase", body: irreversibleBody });
  assert(profileReplay.status === 202 && profileReplay.payload.data?.operation_id === profileOperationID, "SSO profile idempotency replay changed operation");
  await assertSSOOperationVisibility(profileOperationID, sso.a, sso.b, sso.c);
  assertErasedSpace(ssoProfileSpace.id, profileCompleted, profileFixture, "active");
  assertCredentialStatus(ssoProfileCredential.credential.id, "active");
  assertCredentialStatus(ssoCredentialTarget.credential.id, "active");
  assertPreserved(ssoCredentialSpace.id, ssoCredentialFixture);
  assertPreserved(sharedSpace.id, sharedFixture);
  const profilePreservedAfterErase = seedSpaceContent({ teamID: teamAID, ownerID: ssoProfileCredential.credential.id, keyID: ssoProfileCredential.credential.id, spaceID: ssoProfileSpace.id, label: "profile-preserved-after-credential-delete" });

  await placeHold(controlSpace.id, "control_hold");
  await expectStatus(controlErasure(controlSpace.id, "control-held"), 409, "control erasure bypassed legal hold");
  await releaseHold(controlSpace.id);
  const controlQueued = await controlErasure(controlSpace.id, "control-erase");
  assert(controlQueued.status === 202, "control erasure did not return 202");
  const controlOperationID = controlQueued.payload.data?.operation_id;
  const controlCompleted = await waitControlOperation(controlOperationID, "completed");
  const controlReplay = await controlErasure(controlSpace.id, "control-erase");
  assert(controlReplay.status === 202 && controlReplay.payload.data?.operation_id === controlOperationID, "control idempotency replay changed operation");
  const controlDifferentTarget = await controlErasure(idempotencySpace.id, "control-erase");
  assert(controlDifferentTarget.status === 202, "control idempotency key was not scoped to its target");
  const differentTargetOperationID = requiredString(controlDifferentTarget.payload.data?.operation_id, "different-target control operation ID");
  assert(differentTargetOperationID !== controlOperationID, "control idempotency key reused the original target operation");
  const differentTargetCompleted = await waitControlOperation(differentTargetOperationID, "completed");
  assert(differentTargetCompleted.space_id === idempotencySpace.id, "different-target control operation resolved to the wrong space");
  assertErasedSpace(controlSpace.id, controlCompleted, controlFixture, "active");
  assertCredentialStatus(controlTarget.credential.id, "active");
  assertPreserved(ownerBSpace.id, ownerBFixture);

  await acquireKnowledgeIngestLock();
  const recoveryInitial = await ownerRequest(recoveryTarget.apiKey, "owner-recovery-initial");
  assert(recoveryInitial.status === 202, "recovery fixture erasure did not return 202");
  const recoveryInitialID = recoveryInitial.payload.data?.operation_id;
  await waitControlOperation(recoveryInitialID, "processing");
  stopServer();
  await releaseKnowledgeIngestLock();
  postgresExec(`
    UPDATE private_memory_erasure_operations
    SET status = 'failed', attempt_count = 5, worker_id = '', lease_until = NULL,
        next_attempt_at = NULL, last_error_code = 'database_error', completed_at = now(), updated_at = now()
    WHERE id = ${sqlLiteral(recoveryInitialID)}::uuid AND status = 'processing'
  `);
  startServer();
  await waitForServerReady();
  const terminalFailure = await waitControlOperation(recoveryInitialID, "failed");
  assert(terminalFailure.attempt_count === 5 && terminalFailure.last_error_code === "database_error", "terminal recovery fixture was not inspectable");
  assertSpaceState(recoverySpace.id, "sealed", Number(terminalFailure.target_generation) + 1);

  const recoveryQueued = await ownerRequest(recoveryTarget.apiKey, "owner-recovery-reviewed");
  assert(recoveryQueued.status === 202, "reviewed recovery intent did not return 202");
  const recoveryOperationID = recoveryQueued.payload.data?.operation_id;
  assert(recoveryOperationID !== recoveryInitialID, "reviewed recovery intent reused the failed operation");
  const recoveryContract = postgresRow(`
    SELECT current.action, current.target_generation, current.retire_space,
           failed.action, failed.target_generation, failed.retire_space
    FROM private_memory_erasure_operations AS current
    JOIN private_memory_erasure_operations AS failed ON failed.id = ${sqlLiteral(recoveryInitialID)}::uuid
    WHERE current.id = ${sqlLiteral(recoveryOperationID)}::uuid
  `);
  assert(recoveryContract[0] === recoveryContract[3]
    && recoveryContract[1] === recoveryContract[4]
    && recoveryContract[2] === recoveryContract[5], "reviewed recovery changed the sealed erasure target contract");
  const recoveryCompleted = await waitControlOperation(recoveryOperationID, "completed");
  const originalFailure = await waitControlOperation(recoveryInitialID, "failed");
  assert(originalFailure.operation_id === recoveryInitialID, "recovery rewrote the failed operation history");
  assertErasedSpace(recoverySpace.id, recoveryCompleted, recoveryFixture, "active");
  assertCredentialStatus(recoveryTarget.credential.id, "active");

  await assertForeignSSOCredentialDeletionHidden(sso.b, sso.c, ssoCredentialTarget.credential.id);
  await placeHold(ssoCredentialSpace.id, "credential_delete_hold");
  await acquireKnowledgeIngestLock();
  const ssoDeleteQueued = await deleteSSOCredential(sso.a, ssoCredentialTarget.credential.id, "sso-delete-held");
	assert(ssoDeleteQueued.status === 202, "SSO credential deletion did not return 202");
	const ssoDeleteOperationID = ssoDeleteQueued.payload.data?.operation_id;
	assertCredentialStatus(ssoCredentialTarget.credential.id, "disabled");
	const credentialDisableAudit = postgresRow(`
	  SELECT operation, entity_type, before_payload->>'owner_identity_id', before_payload->>'status', actor_role
	  FROM audit_log
	  WHERE team_id = ${sqlLiteral(teamAID)}::uuid
	    AND entity_type = 'api_key'
	    AND entity_id = ${sqlLiteral(ssoCredentialTarget.credential.id)}
	    AND operation = 'REVOKE'
	  ORDER BY timestamp DESC
	  LIMIT 1
	`);
	assert(credentialDisableAudit[0] === "REVOKE"
	  && credentialDisableAudit[1] === "api_key"
	  && credentialDisableAudit[2] === sso.a.identityID
	  && credentialDisableAudit[3] === "active"
	  && credentialDisableAudit[4] === "member", "SSO credential disable did not append its owner audit event");
	await assertCredentialDenied(ssoCredentialTarget.apiKey);
  const heldOperation = postgresRow(`SELECT status FROM private_memory_erasure_operations WHERE id = ${sqlLiteral(ssoDeleteOperationID)}::uuid`);
  assert(heldOperation[0] === "queued", "held SSO credential deletion was claimed before the hold was released");
  await releaseHold(ssoCredentialSpace.id);
  const ssoDeleteReplay = await deleteSSOCredential(sso.a, ssoCredentialTarget.credential.id, "sso-delete-held");
  assert(ssoDeleteReplay.status === 202 && ssoDeleteReplay.payload.data?.operation_id === ssoDeleteOperationID, "disabled SSO credential did not replay its erasure operation");
  await waitControlOperation(ssoDeleteOperationID, "processing");
  const firstClaim = postgresRow(`SELECT fence, attempt_count FROM private_memory_erasure_operations WHERE id = ${sqlLiteral(ssoDeleteOperationID)}::uuid`);
  assert(Number(firstClaim[0]) >= 1 && Number(firstClaim[1]) === 1, "first erasure claim did not establish its fence");

  stopServer();
  await releaseKnowledgeIngestLock();
  assertWriteRejected(ssoCredentialSpace.id, teamAID, ssoCredentialTarget.credential.id, null, "sealed SSO credential space accepted a write");
  postgresExec(`UPDATE private_memory_erasure_operations SET lease_until = now() - interval '1 second' WHERE id = ${sqlLiteral(ssoDeleteOperationID)}::uuid AND status = 'processing'`);
  startServer();
  await waitForServerReady();
  const ssoDeleteCompleted = await waitControlOperation(ssoDeleteOperationID, "completed");
  assert(ssoDeleteCompleted.attempt_count >= 2, "expired erasure lease was not reclaimed");
  const reclaimedFence = Number(postgresScalar(`SELECT fence FROM private_memory_erasure_operations WHERE id = ${sqlLiteral(ssoDeleteOperationID)}::uuid`));
  assert(reclaimedFence > Number(firstClaim[0]), "reclaimed erasure did not advance its fence");
  assertErasedSpace(ssoCredentialSpace.id, ssoDeleteCompleted, ssoCredentialFixture, "retired");
  assertCredentialStatus(ssoCredentialTarget.credential.id, "disabled");
  assertCredentialStatus(ssoProfileCredential.credential.id, "active");
  assertCredentialStatus(ssoSharedCredential.credential.id, "active");
  assertPreserved(ssoProfileSpace.id, profilePreservedAfterErase);
  assertPreserved(sharedSpace.id, sharedFixture);
  await assertSSOOperationVisibility(ssoDeleteOperationID, sso.a, sso.b, sso.c);

  setPrivateContentAge(retentionEligibleSpace.id, 60);
  setPrivateContentAge(retentionHeldSpace.id, 60);
  await placeHold(retentionHeldSpace.id, "retention_hold");
  await updateRetentionDays(30);
  const retentionRun = await runRetention("retention-run-1");
  assert(retentionRun.status === 202 && retentionRun.payload.data?.queued_count === 1, "retention did not queue only the eligible space");
  const retentionReplay = await runRetention("retention-run-1");
  assert(retentionReplay.status === 202 && retentionReplay.payload.data?.id === retentionRun.payload.data?.id, "retention idempotency replay changed run");
  const eligibleOperationID = waitForSpaceOperationID(retentionEligibleSpace.id, "retention");
  const eligibleCompleted = await waitControlOperation(eligibleOperationID, "completed");
  assertErasedSpace(retentionEligibleSpace.id, eligibleCompleted, retentionEligibleFixture, "active");
  assertPreserved(retentionHeldSpace.id, retentionHeldFixture);
  assertSpaceState(retentionHeldSpace.id, "active", 1);

  await releaseHold(retentionHeldSpace.id);
  const heldRetentionRun = await runRetention("retention-run-2");
  assert(heldRetentionRun.status === 202 && heldRetentionRun.payload.data?.queued_count === 1, "released retention hold did not queue the expired space");
  const heldOperationID = waitForSpaceOperationID(retentionHeldSpace.id, "retention");
  const heldCompleted = await waitControlOperation(heldOperationID, "completed");
  assertErasedSpace(retentionHeldSpace.id, heldCompleted, retentionHeldFixture, "active");

  assertPreserved(sharedSpace.id, sharedFixture);
  await assertSharedRecallPreserved(sharedAPIKey, sharedFixture.sentinel);
  await assertCredentialUsable(sharedAPIKey);
  assertNoPrivateContentInTombstones();
  assertNoSensitiveLogs();

  console.log(JSON.stringify({
    status: "ok",
    scenario: "private_memory_erasure",
    tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
    owner_control_retention_paths: true,
    sso_profile_and_credential_paths: true,
    legal_holds_and_idempotency: true,
    abc_non_enumeration: true,
    complete_manifest_zero_counts: true,
    audit_tombstones_redacted: true,
    generation_and_worker_fencing: true,
    expired_lease_reclaimed: true,
    terminal_failure_reauthorized: true,
    team_shared_preserved: true,
  }, null, 2));
}

async function createTeam(name) {
  const response = await controlJSON("/teams", { method: "POST", body: { name, description: "private-memory erasure e2e" } });
  return { id: requiredString(response.data?.id, "created team ID") };
}

async function createControlCredential(teamID, name, memoryBinding) {
  const response = await controlJSON(`/teams/${teamID}/credentials`, {
    method: "POST",
    body: { name, scopes: ["read", "write"], rate_limit: 300, memory_binding: memoryBinding },
  });
  return {
    apiKey: requiredString(response.data?.api_key, "created API key"),
    credential: requiredObject(response.data?.credential, "created credential"),
  };
}

async function createSSOActors(teamCID) {
  const provider = (await controlJSON("/sso/providers", {
    method: "POST",
    body: {
      name: `${runID} IdP`, kind: "generic_oidc", issuer_url: "https://private-erasure-idp.example.test",
      tenant_id: "", identity_claim: "sub", client_id: `${runID}-client`, client_secret_env: "",
      scopes: ["openid", "profile", "email"], group_claims: ["groups"], groups_endpoint: "", groups_scopes: [],
      enabled: true,
    },
  })).data;
  const providerID = requiredString(provider?.id, "SSO provider ID");
  await createSSOMapping(providerID, teamAID, `${runID}-team-a`);
  await createSSOMapping(providerID, teamCID, `${runID}-team-c`);

  const actors = {
    a: ssoActor(providerID, teamAID, `${runID}-actor-a`, `${runID}-team-a`),
    b: ssoActor(providerID, teamAID, `${runID}-actor-b`, `${runID}-team-a`),
    c: ssoActor(providerID, teamCID, `${runID}-actor-c`, `${runID}-team-c`),
  };
  const statements = ["BEGIN", "SELECT set_config('app.tx_mode', 'system', true)"];
  for (const actor of Object.values(actors)) {
    statements.push(`
      INSERT INTO actor_identities (id, kind, team_id, provider, subject, display_name, active)
      VALUES (${sqlLiteral(actor.identityID)}::uuid, 'human', NULL, ${sqlLiteral(providerID)}, ${sqlLiteral(actor.subject)}, ${sqlLiteral(actor.subject)}, true);
      INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active, last_login_at, last_entitlement_check_at)
      VALUES (${sqlLiteral(actor.identityID)}::uuid, ${sqlLiteral(providerID)}::uuid, ${sqlLiteral(actor.subject)}, ${sqlLiteral(actor.subject)}, ${sqlLiteral(`${actor.subject}@example.test`)}, ${sqlLiteral(actor.subject)}, true, now(), now());
      INSERT INTO team_memberships (
        id, actor_identity_id, team_id, status, team_admin, maximum_grants,
        sso_provider_id, sso_group_id, sso_profile_name, sso_entitlement_status,
        sso_last_entitlement_checked_at, sso_last_login_at
      ) VALUES (
        ${sqlLiteral(actor.membershipID)}::uuid, ${sqlLiteral(actor.identityID)}::uuid, ${sqlLiteral(actor.teamID)}::uuid,
        'active', false, ARRAY['read','write']::text[], ${sqlLiteral(providerID)}::uuid,
        ${sqlLiteral(actor.groupID)}, ${sqlLiteral(actor.subject)}, 'active', now(), now()
      );
      INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, credential_id, reason)
      VALUES (${sqlLiteral(actor.teamID)}::uuid, ${sqlLiteral(randomUUID())}::uuid, ${sqlLiteral(actor.identityID)}::uuid, NULL, 'private_erasure_e2e');
      INSERT INTO membership_grants (membership_id, grant_name, source)
      VALUES (${sqlLiteral(actor.membershipID)}::uuid, 'read', 'explicit'), (${sqlLiteral(actor.membershipID)}::uuid, 'write', 'explicit');
      INSERT INTO sso_entitlement_cache (provider_id, subject, groups, status, checked_at, expires_at, error)
      VALUES (${sqlLiteral(providerID)}::uuid, ${sqlLiteral(actor.subject)}, ARRAY[${sqlLiteral(actor.groupID)}]::text[], 'active', now(), now() + interval '8 hours', '');
      INSERT INTO sso_sessions (
        session_hash, identity_id, provider_id, membership_id, team_id, csrf_hash,
        expires_at, created_at, last_seen_at
      ) VALUES (
        ${sqlLiteral(sha256(actor.sessionToken))}, ${sqlLiteral(actor.identityID)}::uuid, ${sqlLiteral(providerID)}::uuid,
        ${sqlLiteral(actor.membershipID)}::uuid, ${sqlLiteral(actor.teamID)}::uuid, ${sqlLiteral(sha256(actor.csrfToken))},
        now() + interval '8 hours', now(), now()
      )
    `);
  }
  statements.push("COMMIT");
  postgresExec(statements.join(";\n"));
  return actors;
}

async function createSSOMapping(providerID, teamID, groupID) {
  await controlJSON(`/sso/providers/${providerID}/mappings`, {
    method: "POST",
    body: { team_id: teamID, group_id: groupID, group_name: groupID, scopes: ["read", "write"], role: "member", enabled: true },
  });
}

function ssoActor(providerID, teamID, subject, groupID) {
  return {
    providerID, teamID, subject, groupID, identityID: randomUUID(), membershipID: randomUUID(),
    sessionToken: `sso-session-${randomUUID()}`, csrfToken: `sso-csrf-${randomUUID()}`,
  };
}

async function assertSSOSession(actor) {
  const response = await ssoRequest(actor, "/ui/api/session");
  assert(response.status === 200 && response.payload.data?.team?.id === actor.teamID, "SSO fixture did not authenticate to its selected team");
}

async function createSSOCredential(actor, name, memoryBinding) {
  const response = await ssoRequest(actor, "/ui/api/sso/credentials", {
    method: "POST",
    body: { name, scopes: ["read", "write"], rate_limit: 300, memory_binding: memoryBinding },
  });
  assert(response.status === 201, "SSO credential creation failed");
  return {
    apiKey: requiredString(response.payload.data?.api_key, "SSO API key"),
    credential: requiredObject(response.payload.data?.credential, "SSO credential"),
  };
}

async function loadSpaces() {
  const response = await controlJSON("/private-memory/spaces?limit=500&offset=0");
  assert(Array.isArray(response.data), "private-memory space collection missing");
  return response.data;
}

function loadTeamSharedSpace(teamID) {
  const id = postgresScalar(`
    SELECT id FROM memory_spaces
    WHERE team_id = ${sqlLiteral(teamID)}::uuid AND kind = 'team_shared'
  `);
  return { id: requiredString(id, "team-shared space ID") };
}

function credentialSpace(spaces, credentialID) {
  return requiredSpace(spaces, (space) => space.kind === "credential_private" && space.owner_credential_id === credentialID, `credential ${credentialID}`);
}

function requiredSpace(spaces, predicate, label) {
  const space = spaces.find(predicate);
  if (!space) throw new Error(`${label} memory space missing`);
  return space;
}

async function assertStrictErasureContract(apiKey) {
  const path = "/ui/api/credential/private-memory";
  for (const [label, options] of [
    ["missing body", { method: "DELETE", idempotencyKey: "strict-missing" }],
    ["missing acknowledgement", { method: "DELETE", idempotencyKey: "strict-empty", body: {} }],
    ["false acknowledgement", { method: "DELETE", idempotencyKey: "strict-false", body: { acknowledge_irreversible: false } }],
    ["unknown field", { method: "DELETE", idempotencyKey: "strict-extra", body: { ...irreversibleBody, extra: true } }],
    ["missing idempotency key", { method: "DELETE", body: irreversibleBody }],
  ]) {
    const response = await apiRequest(apiKey, path, options);
    assert(response.status === 422 && response.payload.code === "VALIDATION_ERROR", `${label} did not fail strict irreversible validation`);
  }
}

async function ownerRequest(apiKey, key) {
  return apiRequest(apiKey, "/ui/api/credential/private-memory", { method: "DELETE", idempotencyKey: key, body: irreversibleBody });
}

async function controlErasure(spaceID, key) {
  return controlRaw(`/private-memory/spaces/${spaceID}/erasures`, { method: "POST", idempotencyKey: key, body: irreversibleBody });
}

async function runRetention(key) {
  return controlRaw("/private-memory/retention-runs", { method: "POST", idempotencyKey: key, body: irreversibleBody });
}

async function deleteSSOCredential(actor, credentialID, key) {
  return ssoRequest(actor, `/ui/api/sso/credentials/${credentialID}`, { method: "DELETE", idempotencyKey: key, body: irreversibleBody });
}

async function placeHold(spaceID, reasonCode) {
  const response = await controlRaw(`/private-memory/spaces/${spaceID}/legal-hold`, { method: "POST", body: { reason_code: reasonCode } });
  assert(response.status === 201, "legal hold was not created");
  const replay = await controlRaw(`/private-memory/spaces/${spaceID}/legal-hold`, { method: "POST", body: { reason_code: reasonCode } });
  assert(replay.status === 200 && replay.payload.data?.id === response.payload.data?.id, "legal hold replay changed the hold");
  const conflict = await controlRaw(`/private-memory/spaces/${spaceID}/legal-hold`, { method: "POST", body: { reason_code: `${reasonCode}_other` } });
  assert(conflict.status === 409, "conflicting active legal hold was accepted");
}

async function releaseHold(spaceID) {
  const response = await controlRaw(`/private-memory/spaces/${spaceID}/legal-hold`, { method: "DELETE" });
  assert(response.status === 200 && response.payload.data?.released === true, "legal hold was not released");
}

async function updateRetentionDays(days) {
  const response = await controlRaw("/config/private-memory", {
    method: "PATCH",
    body: { items: [{ key: "PRIVATE_MEMORY_RETENTION_DAYS", value: String(days) }] },
  });
  assert(response.status === 200 && response.payload.data?.effective?.retention_days === days, "private retention setting was not applied");
}

async function assertOwnerOperationVisibility(operationID, ownerKey, sameTeamKey, otherTeamKey) {
  const owner = await apiRequest(ownerKey, `/ui/api/private-memory/erasures/${operationID}`);
  assert(owner.status === 200 && owner.payload.data?.operation_id === operationID, "credential owner could not read its erasure operation");
  assertOwnerOperationShape(owner.payload.data);
  for (const key of [sameTeamKey, otherTeamKey]) {
    const hidden = await apiRequest(key, `/ui/api/private-memory/erasures/${operationID}`);
    assert(hidden.status === 404 && hidden.payload.code === "NOT_FOUND", "foreign credential enumerated an owner erasure operation");
    assert(!hidden.text.includes(operationID), "foreign credential error reflected an operation ID");
  }
}

async function assertSSOOperationVisibility(operationID, owner, sameTeam, otherTeam) {
  const visible = await ssoRequest(owner, `/ui/api/private-memory/erasures/${operationID}`);
  assert(visible.status === 200 && visible.payload.data?.operation_id === operationID, "SSO owner could not read its erasure operation");
  assertOwnerOperationShape(visible.payload.data);
  for (const actor of [sameTeam, otherTeam]) {
    const hidden = await ssoRequest(actor, `/ui/api/private-memory/erasures/${operationID}`);
    assert(hidden.status === 404 && hidden.payload.code === "NOT_FOUND", "foreign SSO actor enumerated an owner erasure operation");
    assert(!hidden.text.includes(operationID), "foreign SSO error reflected an operation ID");
  }
}

function assertOwnerOperationShape(operation) {
  for (const field of ["team_id", "space_id", "target_credential_id", "attempt_count", "last_error_code"]) {
    assert(!Object.hasOwn(operation ?? {}, field), `owner operation exposed control-only field ${field}`);
  }
}

async function assertForeignSSOCredentialDeletionHidden(sameTeam, otherTeam, credentialID) {
  for (const [actor, key] of [[sameTeam, "foreign-same-team"], [otherTeam, "foreign-other-team"]]) {
    const response = await deleteSSOCredential(actor, credentialID, key);
    assert(response.status === 404 && response.payload.code === "NOT_FOUND", "foreign SSO actor could delete or enumerate a credential");
    assert(!response.text.includes(credentialID), "foreign SSO deletion error reflected the credential ID");
  }
}

async function waitControlOperation(operationID, expectedStatus) {
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    const response = await controlRaw(`/private-memory/erasures/${operationID}`);
    if (response.status === 200 && response.payload.data?.status === expectedStatus) return response.payload.data;
    await delay(250);
  }
  throw new Error(`private-memory operation did not reach ${expectedStatus}`);
}

function waitForSpaceOperationID(spaceID, actorClass) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    const id = postgresScalar(`
      SELECT COALESCE((
        SELECT id::text FROM private_memory_erasure_operations
        WHERE space_id = ${sqlLiteral(spaceID)}::uuid AND actor_class = ${sqlLiteral(actorClass)}
        ORDER BY requested_at DESC LIMIT 1
      ), '')
    `);
    if (id) return id;
    sleepSync(100);
  }
  throw new Error("retention operation was not persisted");
}

function setPrivateContentAge(spaceID, days) {
  postgresExec(`UPDATE memory_spaces SET private_content_at = now() - interval '${Number(days)} days' WHERE id = ${sqlLiteral(spaceID)}::uuid`);
}

function assertCredentialStatus(credentialID, expected) {
  const status = postgresScalar(`SELECT status FROM credentials WHERE id = ${sqlLiteral(credentialID)}::uuid`);
  assert(status === expected, `credential status was ${status}; expected ${expected}`);
}

async function assertCredentialUsable(apiKey) {
  const response = await mcpRaw(apiKey, "tools/list", {});
  assert(response.status === 200 && response.payload.result && !response.payload.error, "active credential was unusable");
}

async function assertCredentialDenied(apiKey) {
  const response = await mcpRaw(apiKey, "tools/list", {});
  assert(response.status === 401 || response.status === 403, "disabled credential remained usable");
}

async function assertRichPrivateVisibility(owner, sameTeam, otherTeam, ownerFixture, sharedFixture) {
  const trace = await mcpToolSuccess(owner.apiKey, "trace_memory", { relationship_id: ownerFixture.relationshipID, include_evidence_content: false });
  assert(JSON.stringify(trace).includes(ownerFixture.relationshipID), "private trace positive control did not return its relationship");
  for (const credential of [sameTeam, otherTeam]) {
    const hidden = await mcpToolRaw(credential.apiKey, "trace_memory", { relationship_id: ownerFixture.relationshipID, include_evidence_content: false });
    assert(hidden.payload.result?.isError === true || hidden.payload.error, "foreign credential traced another private relationship");
  }
  const privateRecall = await recall(owner.apiKey, ownerFixture.sentinel);
  const privateRecallDiagnostic = {
    results: privateRecall.results?.length ?? 0,
    relationships: privateRecall.related_relationships?.length ?? 0,
    search_states: privateRecall.search_states ?? {},
    degradations: (privateRecall.degradations ?? []).map(({ frontier, code }) => ({ frontier, code })),
  };
  assert(JSON.stringify(privateRecall).includes(ownerFixture.sentinel), `private recall positive control did not return its fixture: ${JSON.stringify(privateRecallDiagnostic)}`);
  const sameTeamRecall = await recall(sameTeam.apiKey, ownerFixture.sentinel);
  assert(!JSON.stringify(sameTeamRecall).includes(ownerFixture.sentinel), "same-team credential recalled another private fixture");
  const otherTeamRecall = await recall(otherTeam.apiKey, ownerFixture.sentinel);
  assert(!JSON.stringify(otherTeamRecall).includes(ownerFixture.sentinel), "cross-team credential recalled another private fixture");
  const sharedRecall = await recall(owner.apiKey, sharedFixture.sentinel);
  assert(JSON.stringify(sharedRecall).includes(sharedFixture.sentinel), "private-bound credential could not recall team-shared fixture");
}

async function assertPrivateRecallAbsent(apiKey, sentinel) {
  const result = await recall(apiKey, sentinel);
  assert(!JSON.stringify(result).includes(sentinel), "erased private content remained recallable");
}

async function assertSharedRecallPreserved(apiKey, sentinel) {
  const result = await recall(apiKey, sentinel);
  assert(JSON.stringify(result).includes(sentinel), "team-shared recall changed after private erasure");
}

async function assertTraceAbsent(apiKey, relationshipID) {
  const response = await mcpToolRaw(apiKey, "trace_memory", { relationship_id: relationshipID, include_evidence_content: false });
  if (response.payload.result?.isError === true || response.payload.error) return;
  const text = response.payload.result?.content?.[0]?.text ?? "";
  assert(!text.includes(relationshipID), "erased private relationship remained traceable");
}

async function recall(apiKey, query) {
  return mcpToolSuccess(apiKey, "recall_memory", { query, limit: 20, relationship_limit: 20 });
}

async function mcpToolSuccess(apiKey, name, args) {
  const response = await mcpToolRaw(apiKey, name, args);
  assert(response.status === 200 && !response.payload.error && response.payload.result?.isError !== true, `MCP ${name} returned a bounded error`);
  const content = response.payload.result?.content?.[0]?.text;
  assert(typeof content === "string", `MCP ${name} omitted JSON content`);
  return JSON.parse(content);
}

async function mcpToolRaw(apiKey, name, args) {
  return mcpRaw(apiKey, "tools/call", { name, arguments: args });
}

async function mcpRaw(apiKey, method, params) {
  return requestJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}` },
    body: { jsonrpc: "2.0", id: ++rpcID, method, params },
  });
}

function assertErasedSpace(spaceID, operation, fixture, lifecycle) {
  const catalog = privateMemoryManifestTables();
  const counted = Object.keys(operation.deleted_counts ?? {}).filter((table) => table !== "audit_log" && table !== "v2_migration_corpus_items" && table !== "relationship_cross_references_inbound").sort();
  const missingTables = catalog.filter((table) => !counted.includes(table));
  const unexpectedTables = counted.filter((table) => !catalog.includes(table));
  assert(
    missingTables.length === 0 && unexpectedTables.length === 0,
    `erasure deleted-count manifest did not match the live space catalog: missing=${JSON.stringify(missingTables)} unexpected=${JSON.stringify(unexpectedTables)}`,
  );
  postgresExec(`
    DO $private_erasure_zero$
    DECLARE
      target_table text;
      remaining bigint;
      target_space uuid := ${sqlLiteral(spaceID)}::uuid;
    BEGIN
      FOR target_table IN
        SELECT DISTINCT columns.table_name
        FROM information_schema.columns AS columns
        JOIN information_schema.tables AS tables
          ON tables.table_schema = columns.table_schema AND tables.table_name = columns.table_name
        WHERE columns.table_schema = 'public' AND columns.column_name = 'space_id'
          AND tables.table_type = 'BASE TABLE'
          AND columns.table_name NOT IN ('private_memory_erasure_operations', 'private_memory_legal_holds')
      LOOP
        EXECUTE format('SELECT count(*) FROM %I WHERE space_id = $1', target_table) INTO remaining USING target_space;
        IF remaining <> 0 THEN
          RAISE EXCEPTION 'space-owned rows remain in %', target_table;
        END IF;
      END LOOP;
    END
    $private_erasure_zero$
  `);
  const audit = postgresRow(`
    SELECT count(*), count(*) FILTER (
      WHERE before_payload IS NULL AND after_payload IS NULL
        AND metadata = '{"private_content_erased":true}'::jsonb
    ) FROM audit_log WHERE memory_space_id = ${sqlLiteral(spaceID)}::uuid
  `);
  assert(Number(audit[0]) >= 1 && audit[0] === audit[1], "private audit payload was not reduced to a content-free tombstone");
  assertSpaceState(spaceID, lifecycle, Number(operation.target_generation) + 1);
  const tombstone = postgresScalar(`SELECT to_jsonb(operation)::text FROM private_memory_erasure_operations AS operation WHERE id = ${sqlLiteral(operation.operation_id)}::uuid`);
  for (const value of [fixture.sentinel, ...privateSentinels]) {
    assert(!tombstone.includes(value), "private content survived in an erasure tombstone");
  }
}

function assertSpaceState(spaceID, lifecycle, generation) {
  const state = postgresRow(`SELECT lifecycle_state, generation FROM memory_spaces WHERE id = ${sqlLiteral(spaceID)}::uuid`);
  assert(state[0] === lifecycle && Number(state[1]) === generation, `memory space state was ${state.join("/")}; expected ${lifecycle}/${generation}`);
}

function assertPreserved(spaceID, fixture) {
  const row = postgresRow(`
    SELECT ingest.source_summary, fragment.content
    FROM knowledge_ingests AS ingest
    JOIN evidence_fragments AS fragment ON fragment.team_id = ingest.team_id AND fragment.ingest_id = ingest.ingest_id
    WHERE ingest.ingest_id = ${sqlLiteral(fixture.ingestID)}::uuid AND ingest.space_id = ${sqlLiteral(spaceID)}::uuid
  `);
  assert(row[0] === fixture.sentinel && row[1] === fixture.sentinel, "non-target memory fixture was modified or deleted");
}

function assertWriteRejected(spaceID, teamID, ownerID, generation, message) {
  const ingestID = randomUUID();
  const generationColumn = generation === null ? "" : ", space_generation";
  const generationValue = generation === null ? "" : `, ${Number(generation)}`;
  const result = postgresCommand(`
    BEGIN;
    SELECT set_config('app.tx_mode', 'system', true);
    INSERT INTO knowledge_ingests (
      team_id, ingest_id, owner_profile_id, request_hash, source_summary, status, proposal, metadata, space_id${generationColumn}
    ) VALUES (
      ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerID)}::uuid,
      ${sqlLiteral(`hash-${ingestID}`)}, 'write-fence-probe', 'completed', '{}'::jsonb, '{}'::jsonb,
      ${sqlLiteral(spaceID)}::uuid${generationValue}
    );
    COMMIT
  `);
  assert(result.status !== 0, message);
  const errorSummary = postgresErrorSummary(result);
  assert(
    errorSummary.includes("memory space is not writable") || errorSummary.includes("memory space generation is stale"),
    `write rejection did not report the expected private-memory fence error: ${errorSummary}`,
  );
}

function seedSingleIngest(teamID, ownerID, spaceID, sentinel) {
  const ingestID = randomUUID();
  postgresExec(`
    BEGIN;
    SELECT set_config('app.tx_mode', 'system', true);
    INSERT INTO knowledge_ingests (
      team_id, ingest_id, owner_profile_id, request_hash, source_summary, status, proposal, metadata, space_id
    ) VALUES (
      ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerID)}::uuid,
      ${sqlLiteral(`hash-${ingestID}`)}, ${sqlLiteral(sentinel)}, 'completed', '{}'::jsonb, '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid
    );
    COMMIT
  `);
  return ingestID;
}

function seedSpaceContent({ teamID, ownerID, keyID, spaceID, label, rich = false }) {
  const ids = {
    ingestID: randomUUID(), fragmentID: randomUUID(), securityEventID: randomUUID(), subjectID: randomUUID(), objectID: randomUUID(),
    relationshipID: randomUUID(), observationID: randomUUID(), verificationID: randomUUID(), supportID: randomUUID(),
    conflictID: randomUUID(), positionID: randomUUID(), dreamRunID: randomUUID(), hypothesisID: randomUUID(),
    communityRunID: randomUUID(), communityID: randomUUID(), logicalCommunityID: randomUUID(), searchDocumentID: randomUUID(),
    evidenceSearchDocumentID: randomUUID(), recallID: `${runID}-${label}-${randomUUID()}`,
  };
  const sentinel = `${runID}-${label}-private-content`;
  if (label !== "team-shared") privateSentinels.add(sentinel);
  const sql = [
    "BEGIN",
    "SELECT set_config('app.tx_mode', 'system', true)",
    `INSERT INTO knowledge_ingests (
       team_id, ingest_id, owner_profile_id, idempotency_key, request_hash, source_summary,
       status, proposal, metadata, completed_at, space_id
     ) VALUES (
       ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ids.ingestID)}::uuid, ${sqlLiteral(ownerID)}::uuid,
       ${sqlLiteral(`${runID}-${label}`)}, ${sqlLiteral(`sha256:${ids.ingestID}`)}, ${sqlLiteral(sentinel)},
       'completed', '{}'::jsonb, '{}'::jsonb, now(), ${sqlLiteral(spaceID)}::uuid
     )`,
    `INSERT INTO evidence_fragments (
       team_id, fragment_id, ingest_id, owner_profile_id, evidence_index, content,
       content_hash, source_type, authority, source_ref, space_id
     ) VALUES (
       ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ids.fragmentID)}::uuid, ${sqlLiteral(ids.ingestID)}::uuid,
       ${sqlLiteral(ownerID)}::uuid, 0, ${sqlLiteral(sentinel)}, ${sqlLiteral(`sha256:${ids.fragmentID}`)},
       'manual', 'primary', ${sqlLiteral(`${runID}:${label}`)}, ${sqlLiteral(spaceID)}::uuid
     )`,
    `INSERT INTO audit_log (
       team_id, operation, entity_type, entity_id, before_payload, after_payload, metadata, memory_space_id
     ) VALUES (
       ${sqlLiteral(teamID)}::uuid, 'PRIVATE_FIXTURE', 'memory', ${sqlLiteral(ids.ingestID)},
       jsonb_build_object('content', ${sqlLiteral(sentinel)}), jsonb_build_object('content', ${sqlLiteral(sentinel)}),
       jsonb_build_object('content', ${sqlLiteral(sentinel)}), ${sqlLiteral(spaceID)}::uuid
     )`,
  ];
  if (rich) sql.push(...richSpaceStatements({ ...ids, teamID, ownerID, keyID, spaceID, sentinel, label }));
  sql.push("COMMIT");
  postgresExec(sql.join(";\n"));
  return { ...ids, sentinel };
}

function richSpaceStatements(ids) {
  const q = Object.fromEntries(Object.entries(ids).map(([key, value]) => [key, typeof value === "string" ? sqlLiteral(value) : value]));
  return [
    `INSERT INTO evidence_security_events (
       team_id, security_event_id, fragment_id, ingest_id, owner_profile_id, event_kind, decision, reason, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.securityEventID}::uuid, ${q.fragmentID}::uuid, ${q.ingestID}::uuid,
       ${q.ownerID}::uuid, 'deterministic_scan', 'pass', ${q.sentinel}, '{}'::jsonb, ${q.spaceID}::uuid)`,
    `INSERT INTO evidence_security_signals (
       team_id, security_event_id, signal_index, owner_profile_id, kind, severity, span_start, span_end, quote, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.securityEventID}::uuid, 0, ${q.ownerID}::uuid,
       'instruction_override', 'low', 0, char_length(${q.sentinel}), ${q.sentinel}, '{}'::jsonb, ${q.spaceID}::uuid)`,
    `INSERT INTO team_predicate_definitions (
       team_id, predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
       relationship_kind, current_cardinality, lifecycle_state, origin, metadata, created_at
     ) SELECT ${q.teamID}::uuid, predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
       relationship_kind, current_cardinality, lifecycle_state, 'built_in', metadata, created_at
       FROM predicate_definitions WHERE predicate_key = 'uses' AND version = 1
       ON CONFLICT (team_id, predicate_key, version) DO NOTHING`,
    `INSERT INTO entity_records (team_id, entity_id, entity_kind, metadata, space_id) VALUES
       (${q.teamID}::uuid, ${q.subjectID}::uuid, 'project', jsonb_build_object('content', ${q.sentinel}), ${q.spaceID}::uuid),
       (${q.teamID}::uuid, ${q.objectID}::uuid, 'product', '{}'::jsonb, ${q.spaceID}::uuid)`,
    `INSERT INTO entity_names (team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind, metadata, space_id) VALUES
       (${q.teamID}::uuid, ${q.subjectID}::uuid, ${q.ownerID}::uuid, ${q.sentinel}, lower(${q.sentinel}), 'canonical', '{}'::jsonb, ${q.spaceID}::uuid),
       (${q.teamID}::uuid, ${q.objectID}::uuid, ${q.ownerID}::uuid, ${q.label}, lower(${q.label}), 'canonical', '{}'::jsonb, ${q.spaceID}::uuid)`,
    `INSERT INTO relationship_records (
       team_id, relationship_id, owner_profile_id, semantic_group_key, subject_entity_id,
       predicate_key, predicate_version, object_entity_id, relationship_kind, current_cardinality,
       status, polarity, support_count, source_group_count, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.relationshipID}::uuid, ${q.ownerID}::uuid, ${q.label}, ${q.subjectID}::uuid,
       'uses', 1, ${q.objectID}::uuid, 'state', 'many', 'active', '+', 1, 1,
       jsonb_build_object('content', ${q.sentinel}), ${q.spaceID}::uuid)`,
    `INSERT INTO relationship_observations (
       team_id, observation_id, relationship_id, ingest_id, owner_profile_id, subject_ref,
       original_predicate, object_ref, subject_entity_id, predicate_key, predicate_version,
       object_entity_id, evidence, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.observationID}::uuid, ${q.relationshipID}::uuid, ${q.ingestID}::uuid,
       ${q.ownerID}::uuid, ${q.sentinel}, 'uses', ${q.label}, ${q.subjectID}::uuid, 'uses', 1,
       ${q.objectID}::uuid, jsonb_build_array(jsonb_build_object('fragment_id', ${q.fragmentID}, 'start', 0, 'end', char_length(${q.sentinel}))),
       '{}'::jsonb, ${q.spaceID}::uuid)`,
    `INSERT INTO verification_events (
       team_id, verification_event_id, observation_id, owner_profile_id, evidence_verdict,
       confidence, rationale, model, response_hash, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.verificationID}::uuid, ${q.observationID}::uuid, ${q.ownerID}::uuid,
       'entailed', 0.99, ${q.sentinel}, 'private-erasure-e2e', ${sqlLiteral(`sha256:${ids.verificationID}`)}, '{}'::jsonb, ${q.spaceID}::uuid)`,
    `INSERT INTO relationship_evidence_supports (
       team_id, support_id, relationship_id, observation_id, verification_event_id, fragment_id,
       owner_profile_id, source_group_key, span_start, span_end, quote, authority, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.supportID}::uuid, ${q.relationshipID}::uuid, ${q.observationID}::uuid,
       ${q.verificationID}::uuid, ${q.fragmentID}::uuid, ${q.ownerID}::uuid, ${q.label}, 0,
       char_length(${q.sentinel}), ${q.sentinel}, 'primary', '{}'::jsonb, ${q.spaceID}::uuid)`,
    `INSERT INTO relationship_support_decision_events (
       team_id, support_id, relationship_id, owner_profile_id, actor_profile_id, decision, reason, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.supportID}::uuid, ${q.relationshipID}::uuid, ${q.ownerID}::uuid,
       ${q.ownerID}::uuid, 'grant', ${q.sentinel}, '{}'::jsonb, ${q.spaceID}::uuid)`,
    `INSERT INTO relationship_conflict_cases (
       team_id, conflict_id, semantic_scope_key, status, subject_entity_id, predicate_key, predicate_version,
       relationship_kind, current_cardinality, review_due_at, next_review_at, review_ttl_days,
       policy_version, version, attempts, lease_worker_id, lease_until, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.conflictID}::uuid, ${sqlLiteral(`${ids.label}-${ids.conflictID}`)}, 'open', ${q.subjectID}::uuid,
       'uses', 1, 'state', 'many', now() + interval '1 day', now() + interval '1 day', 7,
       'private_erasure_e2e', 1, 1, 'private-erasure-fixture', now() + interval '1 hour',
       jsonb_build_object('content', ${q.sentinel}), ${q.spaceID}::uuid)`,
    `INSERT INTO relationship_conflict_positions (
       team_id, conflict_id, position_id, position_key, object_entity_id, support_group_count,
       authoritative_group_count, metadata, space_id
     ) VALUES (${q.teamID}::uuid, ${q.conflictID}::uuid, ${q.positionID}::uuid, ${q.label},
       ${q.objectID}::uuid, 1, 1, jsonb_build_object('content', ${q.sentinel}), ${q.spaceID}::uuid)`,
    `INSERT INTO dream_cycle_runs (
       team_id, run_id, initiated_by_profile_id, run_date, window_key, status, lease_until,
       input_count, source_snapshot, space_id
     ) VALUES (${q.teamID}::uuid, ${q.dreamRunID}::uuid, ${q.ownerID}::uuid, CURRENT_DATE::text,
       ${sqlLiteral(`${ids.label}-${ids.dreamRunID}`)}, 'running', now() + interval '1 hour', 1,
       jsonb_build_array(jsonb_build_object('content', ${q.sentinel})), ${q.spaceID}::uuid)`,
    `INSERT INTO hypotheses (
       team_id, hypothesis_id, created_by_profile_id, status, statement, rationale, confidence,
       subject_entity_id, predicate_key, predicate_version, object_entity_id, source_refs,
       source_versions, source_owner_profile_ids, content_hash, cycle_run_id, space_id
     ) VALUES (${q.teamID}::uuid, ${q.hypothesisID}::uuid, ${q.ownerID}::uuid, 'proposed', ${q.sentinel},
       ${q.sentinel}, 0.75, ${q.subjectID}::uuid, 'uses', 1, ${q.objectID}::uuid,
       jsonb_build_array(jsonb_build_object('type','relationship','id',${q.relationshipID})),
       jsonb_build_object(${q.relationshipID}, 1), ARRAY[${q.ownerID}::uuid],
       ${sqlLiteral(`sha256:${ids.hypothesisID}`)}, ${q.dreamRunID}::uuid, ${q.spaceID}::uuid)`,
    `INSERT INTO community_snapshot_runs (
       team_id, run_id, window_key, status, source_snapshot, node_count, edge_count,
       community_count, max_nodes, max_edges, lease_until, space_id
     ) VALUES (${q.teamID}::uuid, ${q.communityRunID}::uuid, ${sqlLiteral(`${ids.label}-${ids.communityRunID}`)},
       'running', jsonb_build_array(jsonb_build_object('content', ${q.sentinel})), 2, 1, 1, 10, 10,
       now() + interval '1 hour', ${q.spaceID}::uuid)`,
    `INSERT INTO community_records (
       team_id, community_id, logical_community_id, run_id, ordinal, status, summary,
       member_count, source_count, top_entities, top_predicates, source_fingerprint, space_id
     ) VALUES (${q.teamID}::uuid, ${q.communityID}::uuid, ${q.logicalCommunityID}::uuid, ${q.communityRunID}::uuid,
       0, 'current', ${q.sentinel}, 1, 1, ARRAY[${q.sentinel}]::text[], ARRAY['uses']::text[],
       ${sqlLiteral(`sha256:${ids.communityID}`)}, ${q.spaceID}::uuid)`,
    `INSERT INTO community_memberships (
       team_id, community_id, entity_id, rank, membership_score, source_count, space_id
     ) VALUES (${q.teamID}::uuid, ${q.communityID}::uuid, ${q.subjectID}::uuid, 0, 1, 1, ${q.spaceID}::uuid)`,
    `INSERT INTO community_sources (
       team_id, community_id, relationship_id, owner_profile_id, relationship_version,
       source_rank, semantic_group_key, source_state_hash, space_id
     ) VALUES (${q.teamID}::uuid, ${q.communityID}::uuid, ${q.relationshipID}::uuid, ${q.ownerID}::uuid,
       1, 0, ${q.label}, ${sqlLiteral(`sha256:${ids.relationshipID}`)}, ${q.spaceID}::uuid)`,
    `INSERT INTO search_documents (
       team_id, search_document_id, owner_profile_id, source_kind, source_id, source_version,
       document_version, embedding_contract_id, embedding_dimensions, search_state,
       document_text, document_hash, metadata, space_id
     ) SELECT ${q.teamID}::uuid, ${q.searchDocumentID}::uuid, ${q.ownerID}::uuid, 'relationship',
       ${q.relationshipID}::uuid, 1, 1, embedding_contract_id, dimensions, 'current', ${q.sentinel},
       ${sqlLiteral(`sha256:${ids.searchDocumentID}`)}, jsonb_build_object('content', ${q.sentinel}), ${q.spaceID}::uuid
       FROM embedding_contracts ORDER BY created_at DESC LIMIT 1`,
    `INSERT INTO search_documents (
       team_id, search_document_id, owner_profile_id, source_kind, source_id, source_version,
       document_version, embedding_contract_id, embedding_dimensions, search_state,
       document_text, document_hash, metadata, space_id
     ) SELECT ${q.teamID}::uuid, ${q.evidenceSearchDocumentID}::uuid, ${q.ownerID}::uuid, 'evidence',
       ${q.fragmentID}::uuid, 1, 1, embedding_contract_id, dimensions, 'current', ${q.sentinel},
       ${sqlLiteral(`sha256:${ids.evidenceSearchDocumentID}`)}, jsonb_build_object('content', ${q.sentinel}), ${q.spaceID}::uuid
       FROM embedding_contracts ORDER BY created_at DESC LIMIT 1`,
    `INSERT INTO recall_feedback_events (
       recall_id, team_id, profile_id, key_id, auth_method, query, tool_args,
       result_refs, result_count, snapshot_state, space_id
     ) VALUES (${q.recallID}, ${q.teamID}::uuid, ${q.ownerID}::uuid, ${q.keyID}::uuid,
       'api_key', ${q.sentinel}, jsonb_build_object('query', ${q.sentinel}),
       jsonb_build_array(jsonb_build_object('id', ${q.relationshipID}, 'type', 'relationship')),
       1, 'captured', ${q.spaceID}::uuid)`,
  ];
}

function privateMemoryManifestTables() {
  if (manifestTables) return manifestTables;
  const value = postgresScalar(`
    SELECT COALESCE(string_agg(table_name, ',' ORDER BY table_name), '')
    FROM (
      SELECT DISTINCT columns.table_name
      FROM information_schema.columns AS columns
      JOIN information_schema.tables AS tables
        ON tables.table_schema = columns.table_schema AND tables.table_name = columns.table_name
      WHERE columns.table_schema = 'public' AND columns.column_name = 'space_id'
        AND tables.table_type = 'BASE TABLE'
        AND columns.table_name NOT IN ('private_memory_erasure_operations', 'private_memory_legal_holds')

      UNION

      SELECT child.relname
      FROM pg_constraint AS constraint_row
      JOIN pg_class AS child ON child.oid = constraint_row.conrelid
      JOIN pg_namespace AS child_namespace ON child_namespace.oid = child.relnamespace
      JOIN pg_class AS parent ON parent.oid = constraint_row.confrelid
      JOIN pg_namespace AS parent_namespace ON parent_namespace.oid = parent.relnamespace
      WHERE constraint_row.contype = 'f'
        AND child_namespace.nspname = 'public'
        AND parent_namespace.nspname = 'public'
        AND parent.relname = 'remember_attempts'
    ) AS catalog
  `);
  manifestTables = value ? value.split(",") : [];
  assert(manifestTables.length >= 50, "live private-memory manifest was unexpectedly small");
  return manifestTables;
}

function assertNoPrivateContentInTombstones() {
  const persisted = postgresScalar(`
    SELECT concat_ws(E'\n',
      COALESCE(string_agg(to_jsonb(operation)::text, E'\n'), ''),
      COALESCE((SELECT string_agg(to_jsonb(hold)::text, E'\n') FROM private_memory_legal_holds AS hold), ''),
      COALESCE((SELECT string_agg(to_jsonb(run)::text, E'\n') FROM private_memory_retention_runs AS run), ''),
      COALESCE((SELECT string_agg(to_jsonb(audit)::text, E'\n')
        FROM audit_log AS audit
        WHERE metadata = '{"private_content_erased":true}'::jsonb), '')
    ) FROM private_memory_erasure_operations AS operation
  `);
  for (const sentinel of privateSentinels) assert(!persisted.includes(sentinel), "private content survived in a retained tombstone");
}

function assertNoSensitiveLogs() {
  const result = composeCommand(["logs", "--no-color", "server"], 20 * 1024 * 1024);
  if (result.status !== 0) throw new Error("could not inspect server logs for private-memory leakage");
  const logs = `${result.stdout}\n${result.stderr}`;
  for (const value of [...privateSentinels, ...sensitiveValues]) {
    assert(!logs.includes(value), "private content or credential material crossed the server log boundary");
  }
}

async function acquireKnowledgeIngestLock() {
  if (tableLock) throw new Error("knowledge-ingest lock already held");
  const applicationName = `private-erasure-lock-${randomUUID()}`;
  const args = [
    "compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "-e", `PGAPPNAME=${applicationName}`,
    "postgres", "sh", "-ec",
    'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "BEGIN; LOCK TABLE knowledge_ingests IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(3600); COMMIT"',
  ];
  const child = spawn("docker", args, { cwd: repositoryRoot(), stdio: ["ignore", "pipe", "pipe"] });
  tableLock = { applicationName, child };
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    const held = postgresScalar(`
      SELECT EXISTS (
        SELECT 1 FROM pg_locks AS lock
        JOIN pg_stat_activity AS activity ON activity.pid = lock.pid
        WHERE activity.application_name = ${sqlLiteral(applicationName)}
          AND lock.relation = 'knowledge_ingests'::regclass
          AND lock.mode = 'AccessExclusiveLock' AND lock.granted
      )
    `);
    if (held === "t" || held === "true") return;
    if (child.exitCode !== null) throw new Error("knowledge-ingest lock process exited early");
    await delay(100);
  }
  throw new Error("timed out acquiring knowledge-ingest erasure fixture lock");
}

async function releaseKnowledgeIngestLock() {
  if (!tableLock) return;
  const { applicationName, child } = tableLock;
  tableLock = null;
  postgresCommand(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = ${sqlLiteral(applicationName)} AND pid <> pg_backend_pid()`);
  child.kill("SIGTERM");
  await Promise.race([
    new Promise((resolve) => child.once("exit", resolve)),
    delay(5_000),
  ]);
}

function stopServer() {
  const result = composeCommand(["stop", "-t", "3", "server"]);
  if (result.status !== 0) throw new Error("could not stop server for erasure worker recovery");
  serverStopped = true;
}

function startServer() {
  const result = composeCommand(["start", "server"]);
  if (result.status !== 0) throw new Error("could not restart server for erasure worker recovery");
  serverStopped = false;
}

async function waitForServerReady() {
  const deadline = Date.now() + 90_000;
  let lastState = "no response";
  while (Date.now() < deadline) {
    try {
      const health = await fetch(`${userURL}/health`);
      const control = await fetch(`${controlURL}/control/api/session`, { headers: { Authorization: `Bearer ${controlToken}` } });
      if (health.ok && control.ok) return;
      lastState = `health=${health.status},control=${control.status}`;
    } catch (error) {
      lastState = error instanceof Error ? error.name : "request failed";
    }
    await delay(250);
  }
  throw new Error(`server did not become ready after erasure worker restart (${lastState})`);
}

async function expectStatus(responsePromise, expected, message) {
  const response = await responsePromise;
  assert(response.status === expected, message);
  return response;
}

async function apiRequest(apiKey, path, options = {}) {
  return requestJSON(`${userURL}${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${apiKey}`, ...(options.headers ?? {}) },
  });
}

async function ssoRequest(actor, path, options = {}) {
  return requestJSON(`${userURL}${path}`, {
    ...options,
    headers: {
      Cookie: `dense_mem_sso_session=${actor.sessionToken}; dense_mem_sso_csrf=${actor.csrfToken}`,
      "X-Dense-Mem-CSRF": actor.csrfToken,
      ...(options.headers ?? {}),
    },
  });
}

async function controlJSON(path, options = {}) {
  const response = await controlRaw(path, options);
  if (response.status < 200 || response.status > 299) throw new Error(`control ${path} returned HTTP ${response.status}; response body redacted`);
  return response.payload;
}

async function controlRaw(path, options = {}) {
  return requestJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${controlToken}`, ...(options.headers ?? {}) },
  });
}

async function requestJSON(url, options = {}) {
  const headers = { Accept: "application/json", ...(options.headers ?? {}) };
  let body;
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = typeof options.body === "string" ? options.body : JSON.stringify(options.body);
  }
  if (options.idempotencyKey !== undefined) headers["Idempotency-Key"] = options.idempotencyKey;
  const response = await fetch(url, { method: options.method ?? "GET", headers, body });
  const text = await response.text();
  let payload = {};
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    throw new Error(`HTTP ${response.status} returned non-JSON content`);
  }
  return { status: response.status, payload, text };
}

function postgresRow(sql) {
  const output = postgresScalar(sql);
  return output.split("|");
}

function postgresScalar(sql) {
  const result = postgresCommand(sql);
  if (result.status !== 0) throw new Error("PostgreSQL private-memory fixture query failed; output redacted");
  return result.stdout.trim();
}

function postgresExec(sql) {
  const result = postgresCommand(sql);
  if (result.status !== 0) throw new Error(`PostgreSQL private-memory fixture mutation failed: ${postgresErrorSummary(result)}`);
}

function postgresErrorSummary(result) {
  const line = String(result.stderr ?? "").split(/\r?\n/).find((item) => item.includes("ERROR:"));
  if (!line) return "error output redacted";
  let summary = line.trim();
  for (const value of [runID, ...privateSentinels, ...sensitiveValues]) {
    summary = summary.replaceAll(value, "[redacted]");
  }
  return summary.slice(0, 500);
}

function postgresCommand(sql) {
  return spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec",
    'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|" -c "$1"',
    "private-memory-erasure-e2e", sql,
  ], { cwd: repositoryRoot(), encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
}

function composeCommand(args, maxBuffer = 2 * 1024 * 1024) {
  return spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, ...args], {
    cwd: repositoryRoot(), encoding: "utf8", maxBuffer,
  });
}

function repositoryRoot() {
  return fileURLToPath(new URL("../..", import.meta.url));
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function requiredString(value, label) {
  if (typeof value !== "string" || !value) throw new Error(`${label} missing`);
  return value;
}

function requiredObject(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} missing`);
  return value;
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function sleepSync(milliseconds) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, milliseconds);
}
