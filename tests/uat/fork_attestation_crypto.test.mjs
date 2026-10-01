import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { test } from "node:test";
import policy from "../../.github/scripts/fork-e2e-policy.cjs";

test("real reusable-workflow signatures bind the artifact, signer, and source", { timeout: 180000 }, async (t) => {
  const lock = JSON.parse(await readFile(new URL("./fork_attestation_source_lock.json", import.meta.url), "utf8"));
  const directory = await mkdtemp(path.join(tmpdir(), "dense-mem-attestation-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  for (const file of lock.files) {
    const response = await fetch(file.url, { signal: AbortSignal.timeout(30000), redirect: "error" });
    assert.equal(response.status, 200, `signed fixture download failed: ${file.name}`);
    const bytes = Buffer.from(await response.arrayBuffer());
    assert.equal(createHash("sha256").update(bytes).digest("hex"), file.sha256, "fixture source lock changed");
    await writeFile(path.join(directory, file.name), bytes);
  }
  const artifact = path.join(directory, "reusable-workflow-artifact");
  const bundleFile = path.join(directory, "reusable-workflow-attestation.sigstore.json");
  const approved = { source_repository: "malancas/attest-demo",
    trusted_revision: "09b495c3f12c7881b3cc17209a327792065c1a1d",
    source_sha: "95baf27389e83e6a5c48f42e190d48d7abcea19e" };
  const args = policy.attestationArguments(artifact, bundleFile, approved);
  args[args.indexOf("--signer-workflow") + 1] = "github/artifact-attestations-workflows/.github/workflows/attest.yml";
  const verify = (arguments_) => spawnSync("gh", arguments_, { encoding: "utf8", timeout: 60000, maxBuffer: 1024 * 1024 });
  const positive = verify(args);
  assert.ifError(positive.error);
  assert.equal(positive.status, 0, positive.stderr);
  assert.ok(JSON.parse(positive.stdout).length > 0);
  for (const [flag, wrongValue] of [["--signer-workflow", policy.SIGNER_WORKFLOW],
    ["--signer-digest", "0".repeat(40)], ["--source-digest", "0".repeat(40)], ["--repo", "attacker/fake-green"]]) {
    const wrong = [...args];
    wrong[wrong.indexOf(flag) + 1] = wrongValue;
    const result = verify(wrong);
    assert.ifError(result.error);
    assert.notEqual(result.status, 0, `${flag} accepted a mismatched signed identity`);
  }
  const bundle = JSON.parse(await readFile(bundleFile, "utf8"));
  const signature = Buffer.from(bundle.dsseEnvelope.signatures[0].sig, "base64");
  signature[0] ^= 1;
  bundle.dsseEnvelope.signatures[0].sig = signature.toString("base64");
  const forgedBundle = path.join(directory, "forged-bundle.json");
  await writeFile(forgedBundle, JSON.stringify(bundle));
  const forgedArgs = [...args];
  forgedArgs[forgedArgs.indexOf("--bundle") + 1] = forgedBundle;
  const forged = verify(forgedArgs);
  assert.ifError(forged.error);
  assert.notEqual(forged.status, 0, "a forged signature was accepted");
  await writeFile(artifact, Buffer.concat([await readFile(artifact), Buffer.from("fabricated green result")]));
  const tampered = verify(args);
  assert.ifError(tampered.error);
  assert.notEqual(tampered.status, 0, "tampered artifact bytes were accepted");
});
