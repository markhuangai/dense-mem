import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";

const root = new URL("../../", import.meta.url);

// The trusted main workflow calls this path on the PR checkout until the Pages migration merges.
test("the wiki publisher and its CI hooks are retired", () => {
  for (const path of [
    ".github/workflows/publish-wiki.yml",
    ".github/scripts/wiki-publication.cjs",
    ".github/scripts/wiki-documentation.cjs",
  ]) {
    assert.equal(existsSync(new URL(path, root)), false, path);
  }

  for (const path of [".github/workflows/ci-shared.yml", "scripts/ci-check.sh"]) {
    const checks = readFileSync(new URL(path, root), "utf8");
    assert.doesNotMatch(checks, /tests\/uat\/wiki_publication\.test\.mjs/, path);
  }
});
