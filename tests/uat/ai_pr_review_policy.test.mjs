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

test("AI review uses seven method-driven goals and one shared protocol", async () => {
  const workflow = await readFile(reviewWorkflowURL, "utf8");
  const normalizedWorkflow = workflow.replace(/\s+/g, " ");
  const promptEntries = workflow.match(/"prompt": \$\{\{ toJSON\(format\(/g) ?? [];

  assert.equal(promptEntries.length, 7);
  assert.match(workflow, /REVIEW_PROTOCOL: >-/);
  assert.doesNotMatch(workflow, /REVIEW_MEMORY_PROTOCOL/);
  assert.match(normalizedWorkflow, /inventory each materially changed behavior/);
  assert.match(normalizedWorkflow, /trace every affected supported path/);
  assert.match(
    normalizedWorkflow,
    /concrete trigger, reachable path, incorrect outcome/,
  );
  assert.match(normalizedWorkflow, /Before returning an empty findings list/);
  assert.match(normalizedWorkflow, /one independently fixable root cause/);
  assert.match(
    normalizedWorkflow,
    /earliest changed line that introduced the cause/,
  );
  assert.doesNotMatch(workflow, /Review proportionality, single responsibility/);
  assert.doesNotMatch(workflow, /Collect workflow analyzer context/);
  assert.doesNotMatch(workflow, /workflow_analysis/);

  for (const goal of [
    "Review scope, architecture, release sequencing, and semantic invariants",
    "Review end-to-end state lifecycle and changed-value propagation",
    "Review authentication, authorization, isolation, RLS, and trust boundaries",
    "Review persistence, migrations, transactions, concurrency, retries, and failure visibility",
    "Review HTTP and MCP contracts, downstream callers, and frontend behavior",
    "Review CI/CD, dependencies, operations, performance, and observability",
    "Review whether tests prove the changed behavior and its failure cases",
  ]) {
    assert.match(workflow, new RegExp(goal.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
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
  assert.match(workflow, /parallel-count: "5"/);
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
