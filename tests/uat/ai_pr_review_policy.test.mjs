import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const agentsURL = new URL("../../AGENTS.md", import.meta.url);
const reviewWorkflowURL = new URL(
  "../../.github/workflows/ai-pr-review.yml",
  import.meta.url,
);
const prCIWorkflowURL = new URL(
  "../../.github/workflows/ci-pr.yml",
  import.meta.url,
);
const sharedWorkflowURL = new URL(
  "../../.github/workflows/ci-shared.yml",
  import.meta.url,
);
const ciCheckURL = new URL("../../scripts/ci-check.sh", import.meta.url);

test("repository guidance requires cross-boundary review and falsification", async () => {
  const agents = await readFile(agentsURL, "utf8");
  const normalizedAgents = agents.replace(/\s+/g, " ");

  assert.match(agents, /## Code Review Rules/);
  assert.match(agents, /### Cross-boundary behavior/);
  assert.match(
    normalizedAgents,
    /trace all affected supported paths from input or writer through validation/,
  );
  assert.match(normalizedAgents, /preserves one authoritative value end to end/);
  assert.match(agents, /### Falsification/);
  assert.match(normalizedAgents, /construct a concrete supported counterexample/);
  assert.match(
    normalizedAgents,
    /reject hypothetical, pre-existing, or contradicted claims/,
  );
  assert.match(normalizedAgents, /semantic verifier\/reviewer behavior/);
});

test("AI review uses six ownership-driven goals and one memory-first protocol", async () => {
  const workflow = await readFile(reviewWorkflowURL, "utf8");
  const normalizedWorkflow = workflow.replace(/\s+/g, " ");
  const promptEntries = workflow.match(/"prompt": \$\{\{ toJSON\(format\(/g) ?? [];

  assert.equal(promptEntries.length, 6);
  assert.match(workflow, /REVIEW_PROTOCOL: >-/);
  assert.doesNotMatch(workflow, /REVIEW_MEMORY_PROTOCOL/);
  assert.match(normalizedWorkflow, /focused query for this goal/);
  assert.match(normalizedWorkflow, /active release and version decisions/);
  assert.match(normalizedWorkflow, /prior accepted or rejected review findings/);
  assert.match(normalizedWorkflow, /known false-positive patterns/);
  assert.match(normalizedWorkflow, /project context, not sole proof/);
  assert.match(
    normalizedWorkflow,
    /memory is unavailable, stale, degraded, or has no relevant result/,
  );
  assert.match(
    normalizedWorkflow,
    /Confirm every reported defect against changed code on a supported reachable path/,
  );
  assert.match(
    normalizedWorkflow,
    /For each changed field, state, identifier, configuration value, or contract owned by this goal/,
  );
  assert.match(
    normalizedWorkflow,
    /trace all supported inputs and writers through validation, persistence or serialization, asynchronous work, and every reader or consumer/,
  );
  assert.match(
    normalizedWorkflow,
    /drops, defaults, overwrites, mis-scopes, or retains a stale authoritative value/,
  );
  assert.match(normalizedWorkflow, /Report only root causes owned by this goal/);
  assert.match(
    normalizedWorkflow,
    /Use applicable real-logic tests and required evaluation evidence to confirm or falsify behavior claims/,
  );
  assert.match(
    normalizedWorkflow,
    /Only the test-assurance goal may report missing or vacuous test, evaluation, and end-to-end coverage/,
  );
  assert.doesNotMatch(workflow, /Before returning an empty findings list/);
  assert.doesNotMatch(workflow, /construct at least one concrete failure scenario/);
  assert.doesNotMatch(workflow, /dormant[- ]V2/);
  assert.doesNotMatch(workflow, /Collect workflow analyzer context/);
  assert.doesNotMatch(workflow, /workflow_analysis/);
  assert.match(
    normalizedWorkflow,
    /PostgreSQL must remain the only durable authority, Neo4j must remain migration input only/,
  );

  for (const goal of [
    "Review functional and semantic correctness plus bounded design integrity, including model-provider boundaries",
    "Review authentication, authorization, isolation, privacy, and trust boundaries",
    "Review durable-state integrity and distributed reliability",
    "Review HTTP and MCP contracts plus user-facing behavior",
    "Review scope, release compatibility, performance, and operational readiness",
    "Review test assurance for every new or changed supported behavior",
  ]) {
    assert.match(workflow, new RegExp(goal.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
});

test("maintainability review is preventive, bounded, and impact-scored", async () => {
  const workflow = await readFile(reviewWorkflowURL, "utf8");
  const normalizedWorkflow = workflow.replace(/\s+/g, " ");

  assert.match(normalizedWorkflow, /duplication of the same authoritative rule/);
  assert.match(normalizedWorkflow, /supported sites change for the same reason/);
  assert.match(normalizedWorkflow, /narrower dependency-safe boundary/);
  assert.match(
    normalizedWorkflow,
    /responsibilities A and B with independent change triggers/,
  );
  assert.match(
    normalizedWorkflow,
    /Own these bounded design-only concerns across repository domains, including authentication, authorization, durable state, transactions, concurrency, HTTP and MCP contracts, frontend, deployment, and operations/,
  );
  assert.match(
    normalizedWorkflow,
    /similar syntax that implements different boundary-specific policy/,
  );
  assert.match(
    normalizedWorkflow,
    /an extraction that broadens an API or creates cross-layer coupling/,
  );
  assert.match(normalizedWorkflow, /leave that functional defect to its designated goal rather than duplicating it/);
  assert.match(normalizedWorkflow, /Use Low severity by default/);
  assert.match(
    normalizedWorkflow,
    /That exclusion does not apply to the bounded design-only concerns owned above/,
  );
});

test("test-assurance review owns positive, negative, and local E2E proof", async () => {
  const workflow = await readFile(reviewWorkflowURL, "utf8");
  const normalizedWorkflow = workflow.replace(/\s+/g, " ");

  assert.match(normalizedWorkflow, /Build a scenario map from the linked issue/);
  assert.match(
    normalizedWorkflow,
    /concrete positive coverage and a distinct negative, rejection, boundary, failure, recovery, or regression case/,
  );
  assert.match(normalizedWorkflow, /existing tests count when their setup and assertions exercise the changed path/);
  assert.match(
    normalizedWorkflow,
    /real PostgreSQL-backed service integration coverage for RLS, migrations, constraints, transactions, locks, idempotency, pgvector, concurrency, and cross-profile behavior/,
  );
  assert.match(
    normalizedWorkflow,
    /mocks may isolate outbound provider transport failures or pure query construction, but must still exercise real validation and domain policy around those boundaries/,
  );
  assert.match(
    normalizedWorkflow,
    /with a supported local production-entry harness, require an applicable committed local Compose or UAT end-to-end scenario/,
  );
  assert.match(
    normalizedWorkflow,
    /committed local Compose or UAT end-to-end scenario through production entry points and real dependencies/,
  );
  assert.match(
    normalizedWorkflow,
    /For GitHub Actions or external-host workflow behavior that the local harness cannot execute, require the repository-prescribed real-logic policy test and static validation instead of Compose or UAT/,
  );
  assert.match(
    normalizedWorkflow,
    /compose-backed Playwright coverage of the built image in desktop and mobile projects/,
  );
  assert.match(
    normalizedWorkflow,
    /presentation constraints such as visibility, clipping, overlap, and horizontal overflow/,
  );
  assert.match(
    normalizedWorkflow,
    /Do not require screenshot baselines, CI execution, or PR-reported run output/,
  );
  assert.match(
    normalizedWorkflow,
    /behavior-preserving refactors, or internal changes with no affected observable invariant/,
  );
  assert.match(
    normalizedWorkflow,
    /name the missing scenario, trigger, and expected observable result/,
  );
});

test("review safety controls and CI policy coverage remain enabled", async () => {
  const [workflow, sharedWorkflow, ciCheck] = await Promise.all([
    readFile(reviewWorkflowURL, "utf8"),
    readFile(sharedWorkflowURL, "utf8"),
    readFile(ciCheckURL, "utf8"),
  ]);

  assert.ok(
    workflow.includes(
      "pull-request-url: ${{ format('{0}/{1}/pull/{2}', github.server_url, github.repository, needs.resolve.outputs.pr_number) }}",
    ),
  );
  assert.doesNotMatch(
    workflow,
    /pull-request-number: \$\{\{ needs\.resolve\.outputs\.pr_number \}\}/,
  );
  assert.doesNotMatch(
    workflow,
    /expected-head-sha: \$\{\{ needs\.resolve\.outputs\.head_sha \}\}/,
  );
  assert.match(workflow, /EXPECTED_HEAD_SHA: \$\{\{ needs\.resolve\.outputs\.head_sha \}\}/);
  assert.match(workflow, /persist-credentials: false/);
  assert.match(workflow, /effort: high/);
  assert.match(workflow, /parallel-count: "6"/);
  assert.match(workflow, /max-turns: "100"/);
  assert.match(workflow, /auto-approve: "false"/);
  assert.match(workflow, /permission_policy: always_allow/);
  assert.match(workflow, /org_max_permission: allow/);
  assert.match(
    sharedWorkflow,
    /node --test tests\/uat\/ai_pr_review_policy\.test\.mjs/,
  );
  assert.match(ciCheck, /node --test tests\/uat\/ai_pr_review_policy\.test\.mjs/);
});

test("AI review selects the configured executor and matching endpoint", async () => {
  const workflow = await readFile(reviewWorkflowURL, "utf8");
  const validationStart = workflow.indexOf(
    "      - name: Validate AI reviewer configuration",
  );
  const validationEnd = workflow.indexOf(
    "      - name: Checkout pull request head",
    validationStart,
  );

  assert.notEqual(validationStart, -1);
  assert.notEqual(validationEnd, -1);

  const validationStep = workflow.slice(validationStart, validationEnd);
  assert.match(validationStep, /AI_REVIEWER_SDK: \$\{\{ vars\.AI_REVIEWER_SDK \}\}/);
  assert.match(
    validationStep,
    /codex\)\s+endpoint="\$\{LOCAL_OPENAI_AI_ENDPOINT\}"/,
  );
  assert.match(validationStep, /claude\)\s+endpoint="\$\{LOCAL_AI_ENDPOINT\}"/);
  assert.match(validationStep, /AI_REVIEWER_SDK must be exactly 'codex' or 'claude'/);
  assert.match(validationStep, /\[\[ -z "\$\{endpoint\}" \]\]/);
  assert.ok(workflow.includes("executor: ${{ vars.AI_REVIEWER_SDK }}"));
  assert.ok(
    workflow.includes(
      "ai-base-url: ${{ vars.AI_REVIEWER_SDK == 'codex' && secrets.LOCAL_OPENAI_AI_ENDPOINT || secrets.LOCAL_AI_ENDPOINT }}",
    ),
  );
});

test("owner-controlled AI review starts with PR CI while untrusted heads use the CI fallback", async () => {
  const [reviewWorkflow, ciWorkflow] = await Promise.all([
    readFile(reviewWorkflowURL, "utf8"),
    readFile(prCIWorkflowURL, "utf8"),
  ]);
  const normalizedReviewWorkflow = reviewWorkflow.replace(/\s+/g, " ");
  const normalizedCIWorkflow = ciWorkflow.replace(/\s+/g, " ");

  assert.match(reviewWorkflow, /^\s+pull_request_target:/m);
  assert.match(reviewWorkflow, /^\s+workflow_run:/m);
  assert.match(reviewWorkflow, /^\s+workflow_dispatch:/m);
  assert.match(
    normalizedReviewWorkflow,
    /pull_request_target: branches: - main types: \[opened, synchronize, reopened, ready_for_review\]/,
  );
  assert.match(
    normalizedCIWorkflow,
    /pull_request: branches: - main types: \[opened, synchronize, reopened, ready_for_review\]/,
  );
  assert.match(
    normalizedReviewWorkflow,
    /workflows: - CI Pull Request types: \[completed\]/,
  );
  assert.match(reviewWorkflow, /github\.event_name == 'pull_request_target'/);
  assert.match(
    reviewWorkflow,
    /TRIGGERING_ACTOR: \$\{\{ github\.triggering_actor \}\}/,
  );
  assert.match(reviewWorkflow, /const trustedActor = "Z-M-Huang"/);
  assert.match(reviewWorkflow, /eventName === "pull_request_target"/);
  assert.match(reviewWorkflow, /const eventPull = context\.payload\.pull_request/);
  assert.match(
    normalizedReviewWorkflow,
    /eventPull\.user\?\.login === trustedActor && context\.actor === trustedActor && process\.env\.TRIGGERING_ACTOR === trustedActor/,
  );
  assert.match(reviewWorkflow, /const eventHeadRepositoryId = eventPull\.head\?\.repo\?\.id/);
  assert.match(reviewWorkflow, /const eventBaseRepositoryId = eventPull\.base\?\.repo\?\.id/);
  assert.match(
    normalizedReviewWorkflow,
    /Number\.isSafeInteger\(eventHeadRepositoryId\) && eventHeadRepositoryId === eventBaseRepositoryId/,
  );
  assert.match(reviewWorkflow, /pullNumber = eventPull\.number/);
  assert.match(reviewWorkflow, /triggerHeadSha = eventPull\.head\?\.sha/);
  assert.match(reviewWorkflow, /typeof triggerHeadSha !== "string"/);
  assert.match(reviewWorkflow, /workflowRun\.conclusion !== "success"/);
  assert.match(reviewWorkflow, /workflowRunActor = workflowRun\.actor\?\.login/);
  assert.match(
    reviewWorkflow,
    /workflowRunTriggeringActor = workflowRun\.triggering_actor\?\.login/,
  );
  assert.match(reviewWorkflow, /const triggerHeadRef = workflowRun\.head_branch/);
  assert.match(
    reviewWorkflow,
    /const triggerHeadRepositoryId = workflowRun\.head_repository\?\.id/,
  );
  assert.match(reviewWorkflow, /typeof triggerHeadRef !== "string"/);
  assert.match(reviewWorkflow, /!Number\.isSafeInteger\(triggerHeadRepositoryId\)/);
  assert.match(
    normalizedReviewWorkflow,
    /pull\.head\.sha === triggerHeadSha && pull\.head\.ref === triggerHeadRef && pull\.head\.repo\?\.id === triggerHeadRepositoryId/,
  );
  assert.match(
    normalizedReviewWorkflow,
    /pull\.user\?\.login === trustedActor && pullIsSameRepository && workflowRunActor === trustedActor && workflowRunTriggeringActor === trustedActor/,
  );
  assert.match(
    normalizedReviewWorkflow,
    /eventName === "workflow_run" && pullUsesDirectReview/,
  );
  assert.match(reviewWorkflow, /pull\.head\.sha !== triggerHeadSha/);
});

test("AI review status identity remains scoped to the resolved pull request", async () => {
  const workflow = await readFile(reviewWorkflowURL, "utf8");
  const normalizedWorkflow = workflow.replace(/\s+/g, " ");
  const statusWrites =
    workflow.match(/context: process\.env\.REVIEW_STATUS_CONTEXT/g) ?? [];

  assert.match(workflow, /REVIEW_STATUS_CONTEXT_PREFIX: AI PR review/);
  assert.match(
    workflow,
    /review_status_context: \$\{\{ steps\.resolve\.outputs\.review_status_context \}\}/,
  );
  assert.match(
    normalizedWorkflow,
    /const reviewStatusContext = `\$\{process\.env\.REVIEW_STATUS_CONTEXT_PREFIX\} \/ PR #\$\{pull\.number\}`/,
  );
  assert.match(workflow, /status\.context === reviewStatusContext/);
  assert.match(
    workflow,
    /core\.setOutput\("review_status_context", reviewStatusContext\)/,
  );
  assert.match(
    workflow,
    /REVIEW_STATUS_CONTEXT: \$\{\{ needs\.resolve\.outputs\.review_status_context \}\}/,
  );
  assert.equal(statusWrites.length, 2);
  assert.doesNotMatch(
    workflow,
    /status\.context === process\.env\.REVIEW_STATUS_CONTEXT/,
  );
});

test("final review status is fenced by the live pull request head", async () => {
  const workflow = await readFile(reviewWorkflowURL, "utf8");
  const finalStatusStep = workflow.slice(
    workflow.indexOf("      - name: Publish final review status"),
  );

  assert.match(
    finalStatusStep,
    /PR_NUMBER: \$\{\{ needs\.resolve\.outputs\.pr_number \}\}/,
  );
  assert.match(finalStatusStep, /await github\.rest\.pulls\.get\(\{/);
  assert.match(
    finalStatusStep,
    /pull_number: Number\(process\.env\.PR_NUMBER\)/,
  );
  assert.match(
    finalStatusStep,
    /pull\.head\.sha !== process\.env\.HEAD_SHA/,
  );

  const liveHeadRead = finalStatusStep.indexOf("github.rest.pulls.get");
  const headComparison = finalStatusStep.indexOf(
    "pull.head.sha !== process.env.HEAD_SHA",
  );
  const staleRunFailure = finalStatusStep.indexOf("core.setFailed", headComparison);
  const staleRunReturn = finalStatusStep.indexOf("return;", headComparison);
  const statusPublication = finalStatusStep.indexOf(
    "github.rest.repos.createCommitStatus",
  );
  assert.ok(liveHeadRead < headComparison);
  assert.ok(headComparison < staleRunFailure);
  assert.ok(staleRunFailure < staleRunReturn);
  assert.ok(staleRunReturn < statusPublication);
});
