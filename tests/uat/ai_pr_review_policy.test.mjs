import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const agentsURL = new URL("../../AGENTS.md", import.meta.url);
const reviewWorkflowURL = new URL(
  "../../.github/workflows/ai-pr-review.yml",
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
  assert.match(normalizedWorkflow, /Report only root causes owned by this goal/);
  assert.match(
    normalizedWorkflow,
    /For behavior owned by this goal, inspect the applicable real-logic tests and required evaluation evidence/,
  );
  assert.doesNotMatch(workflow, /Before returning an empty findings list/);
  assert.doesNotMatch(workflow, /construct at least one concrete failure scenario/);
  assert.doesNotMatch(workflow, /Review whether tests prove the changed behavior/);
  assert.doesNotMatch(workflow, /dormant[- ]V2/);
  assert.doesNotMatch(workflow, /Collect workflow analyzer context/);
  assert.doesNotMatch(workflow, /workflow_analysis/);
  assert.match(
    normalizedWorkflow,
    /PostgreSQL must remain the only durable authority, Neo4j must remain migration input only/,
  );

  for (const goal of [
    "Review functional and semantic correctness, including model-provider boundaries",
    "Review authentication, authorization, isolation, privacy, and trust boundaries",
    "Review durable-state integrity and distributed reliability",
    "Review HTTP and MCP contracts plus user-facing behavior",
    "Review scope, release compatibility, performance, and operational readiness",
    "Review design integrity, semantic duplication, and single responsibility",
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
    /similar syntax that implements different boundary-specific policy/,
  );
  assert.match(
    normalizedWorkflow,
    /an extraction that broadens an API or creates cross-layer coupling/,
  );
  assert.match(normalizedWorkflow, /leave it to that functional goal rather than duplicating it/);
  assert.match(normalizedWorkflow, /Use Low severity by default/);
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

test("automatic AI review waits for successful current-head PR CI", async () => {
  const workflow = await readFile(reviewWorkflowURL, "utf8");
  const normalizedWorkflow = workflow.replace(/\s+/g, " ");

  assert.doesNotMatch(workflow, /^\s+pull_request_target:/m);
  assert.match(workflow, /^\s+workflow_run:/m);
  assert.match(workflow, /^\s+workflow_dispatch:/m);
  assert.match(normalizedWorkflow, /workflows: - CI Pull Request types: \[completed\]/);
  assert.match(
    normalizedWorkflow,
    /github\.event_name == 'workflow_dispatch' \|\| \(github\.event\.workflow_run\.event == 'pull_request' && github\.event\.workflow_run\.conclusion == 'success'\)/,
  );
  assert.match(workflow, /workflowRun\.event !== "pull_request"/);
  assert.match(workflow, /workflowRun\.conclusion !== "success"/);
  assert.match(workflow, /const triggerHeadRef = workflowRun\.head_branch/);
  assert.match(
    workflow,
    /const triggerHeadRepositoryId = workflowRun\.head_repository\?\.id/,
  );
  assert.match(workflow, /typeof triggerHeadRef !== "string"/);
  assert.match(workflow, /!Number\.isSafeInteger\(triggerHeadRepositoryId\)/);
  assert.match(
    normalizedWorkflow,
    /pull\.head\.sha === triggerHeadSha && pull\.head\.ref === triggerHeadRef && pull\.head\.repo\?\.id === triggerHeadRepositoryId/,
  );
  assert.match(
    workflow,
    /pull\.head\.sha !== triggerHeadSha/,
  );
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
