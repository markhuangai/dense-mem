import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";

const require = createRequire(new URL("../../.lint/package.json", import.meta.url));
export const { load: parseWorkflow, dump: stringifyWorkflow } = require("js-yaml");

export async function readWorkflow(name) {
  return parseWorkflow(await readFile(new URL("../../.github/workflows/" + name, import.meta.url), "utf8"));
}

export function workflowJob(workflow, name) {
  const job = workflow.jobs?.[name];
  assert.ok(job, `workflow job ${name} is missing`);
  return job;
}

export function actionSteps(job, action) {
  const matches = (job.steps ?? []).filter(({ uses }) => {
    if (typeof uses !== "string") return false;
    const [identity, ref, extra] = uses.split("@");
    return identity === action && ref && !/\s/.test(ref) && extra === undefined;
  });
  assert.ok(matches.length > 0, "missing " + action + " action");
  return matches;
}

export function actionStep(job, action) {
  const matches = actionSteps(job, action);
  assert.equal(matches.length, 1, `expected one ${action} action`);
  return matches[0];
}

export function runStep(job, command) {
  const matches = (job.steps ?? []).filter(({ run }) => typeof run === "string" && run.includes(command));
  assert.equal(matches.length, 1, `expected one step running ${command}`);
  return matches[0];
}

export function jobScripts(job) {
  return (job.steps ?? []).flatMap((step) => [step.run, step.with?.script])
    .filter((script) => typeof script === "string").join("\n");
}
