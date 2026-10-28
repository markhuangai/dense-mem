import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { test } from "node:test";

for (const [variant, service] of [["base", "server"], ["expert", "server"], ["demo", "demo"]]) {
  test(`${variant} deployment forwards independent decision configuration`, () => {
    const expected = {
      AI_DECISION_PROVIDER: "typesafe",
      AI_DECISION_API_URL: "https://api.typesafe.ai/v1",
      AI_DECISION_API_KEY: "decision-deployment-fixture",
      AI_DECISION_MODEL: "jev-1.14.0",
      AI_DECISION_MAX_INPUT_TOKENS: "64000",
      AI_DECISION_MAX_QUESTION_INPUT_TOKENS: "32000",
      AI_DECISION_TIMEOUT_SECONDS: "10",
      AI_DECISION_MAX_CONCURRENCY: "4",
      AI_DECISION_LONG_CONTEXT_ENABLED: "false",
    };
    const env = { ...process.env, POSTGRES_PASSWORD: "database-deployment-fixture", CONTROL_PORTAL_TOKEN: "control-deployment-fixture", AI_API_KEY: "generative-deployment-fixture", AI_VERIFIER_MODEL: "gpt-4o-mini", ...expected };
    const render = () => {
      const result = spawnSync("docker", ["compose", "-f", `examples/docker-compose.${variant}.yml`, "config", "--format", "json"], { env, encoding: "utf8" });
      assert.equal(result.status, 0, "deployment configuration must render");
      return JSON.parse(result.stdout).services[service].environment;
    };
    const configured = render();
    for (const [key, value] of Object.entries(expected)) assert.equal(configured[key], value, key);
    assert.notEqual(configured.AI_DECISION_API_KEY, configured.AI_API_KEY);
    env.AI_DECISION_PROVIDER = "";
    assert.equal(render().AI_DECISION_PROVIDER, "disabled");
  });
}
