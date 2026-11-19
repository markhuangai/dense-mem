import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { requireRunner, sha256 } from "../../../.github/scripts/evaluation-policy.mjs";
import { buildBootableSeed, dumpSeed, extractSeed, hashFile, restoreSeed, sealSeed, verifySeedFiles, verifySeedCredentialExclusions, excludedSeedData, postgresImage } from "../../../.github/scripts/evaluation-seeds.mjs";
import { docker, location } from "./workflow.mjs";

requireRunner(process.env);

test("production seed exporter preserves data, excludes payloads and protects restore failures", { timeout: 240_000 }, async (t) => {
  const dataset = `seed_export_${randomUUID().replaceAll("-", "")}`;
  const env = { ...process.env, EVAL_MODE: "fixtures", EVAL_DATASET: dataset };
  const paths = location(env);
  const container = `densemem-eval-${paths.runID}-postgres`;
  const seedDir = path.join(paths.directory, "seed");
  const canary = `seed-password-${randomUUID()}`;
  const label = "io.dense-mem.eval.run";
  fs.mkdirSync(seedDir, { recursive: true, mode: 0o700 });
  const diagnostic = path.join(seedDir, "postgres-failure.log");
  fs.writeFileSync(paths.stateFile, JSON.stringify({ runID: paths.runID, environment: { EVAL_DATABASE_PASSWORD: canary } }), { mode: 0o600 });
  fs.writeFileSync(paths.manifestFile, JSON.stringify({ version: 1, run_id: paths.runID, workflow_run_id: env.GITHUB_RUN_ID, source_revision: "b".repeat(40), image: `ghcr.io/markhuangai/dense-mem@sha256:${"a".repeat(64)}`, registry_sha256: sha256(fs.readFileSync("tests/eval/fixtures/datasets.json")), dataset, mode: "fixtures", operation: "run", model: "fixture", embedding_model: "text-embedding-3-small", embedding_dimensions: 1536, context_window_tokens: 300000, seed_dir: seedDir, data_dir: seedDir, output_dir: path.join(paths.directory, "public"), model_deadline: new Date(Date.now() + 60_000).toISOString() }), { mode: 0o600 });
  const protectedDiagnostics = () => {
    const result = spawnSync(process.execPath, ["tests/eval/scripts/workflow.mjs", "diagnostics"], { encoding: "utf8", env, timeout: 30_000 });
    assert.equal(result.status, 0, "actual workflow diagnostics must protect PostgreSQL failures without a running server");
    const output = result.stdout + result.stderr;
    for (const [format, value] of [["raw", canary], ["base64", Buffer.from(canary).toString("base64")], ["hex", Buffer.from(canary).toString("hex")], ["uppercase hex", Buffer.from(canary).toString("hex").toUpperCase()]]) assert.equal(output.includes(value), false, `${format} credential remained in protected output`);
    return output;
  };
  t.after(() => {
    try {
      if (fs.existsSync(diagnostic)) process.stdout.write(protectedDiagnostics());
    } finally {
      if (docker(["container", "ls", "--all", "--filter", `name=^${container}$`, "--quiet"])) {
        const actual = JSON.parse(docker(["container", "inspect", container]))[0];
        assert.equal(actual.Config.Labels[label], paths.runID);
        docker(["container", "rm", "--force", "--volumes", actual.Id]);
      }
      fs.rmSync(paths.directory, { recursive: true });
    }
  });
  const pgEnv = path.join(paths.directory, "postgres.env");
  fs.writeFileSync(pgEnv, `POSTGRES_USER=densemem\nPOSTGRES_DB=densemem\nPOSTGRES_PASSWORD=${canary}\n`, { mode: 0o600 });
  docker(["run", "--detach", "--name", container, "--label", `${label}=${paths.runID}`, "--env-file", pgEnv, postgresImage]);
  const deadline = Date.now() + 60_000;
  while (spawnSync("docker", ["exec", container, "pg_isready", "--host=127.0.0.1", "-U", "densemem"], { stdio: "ignore" }).status !== 0) {
    assert.ok(Date.now() < deadline, "seed exporter PostgreSQL readiness timed out");
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
  const sql = (statement) => docker(["exec", "--interactive", container, "psql", "--username=densemem", "--dbname=densemem", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--tuples-only", "--no-align"], { input: statement });
  const resetDatabase = () => docker(["exec", "--interactive", container, "psql", "--username=densemem", "--dbname=postgres", "--no-psqlrc", "--set=ON_ERROR_STOP=1"], { input: "DROP DATABASE densemem WITH (FORCE); CREATE DATABASE densemem;" });
  const team = randomUUID(), space = randomUUID();
  sql(`CREATE EXTENSION vector;
    CREATE TABLE retained_evidence (id integer PRIMARY KEY, content text NOT NULL, embedding vector(3));
    INSERT INTO retained_evidence VALUES (1, 'exact public Evidence', '[1,2,3]');
    CREATE INDEX retained_evidence_vector ON retained_evidence USING hnsw (embedding vector_l2_ops);
    CREATE FUNCTION seed_copy_delay() RETURNS boolean LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.3); RETURN true; END $$;
    CREATE TABLE memory_spaces (id uuid PRIMARY KEY, team_id uuid NOT NULL, kind text NOT NULL, CHECK(seed_copy_delay()));
    INSERT INTO memory_spaces VALUES ('${space}', '${team}', 'team_shared');
    CREATE FUNCTION dense_mem_team_shared_space(team uuid) RETURNS uuid LANGUAGE plpgsql STABLE SET search_path = public AS $$
      DECLARE result uuid; BEGIN SELECT id INTO result FROM memory_spaces WHERE team_id=team AND kind='team_shared';
      IF result IS NULL THEN RAISE EXCEPTION 'team shared memory space is not initialized'; END IF; RETURN result; END $$;
    CREATE TABLE ontology_maintenance_markers (team_id uuid NOT NULL, space_id uuid NOT NULL, CHECK(space_id=dense_mem_team_shared_space(team_id)));
    INSERT INTO ontology_maintenance_markers VALUES ('${team}', '${space}');
    ${excludedSeedData.map((table) => `CREATE TABLE ${table} (payload text); INSERT INTO ${table} VALUES ('${canary}');`).join("\n")}`);
  fs.writeFileSync(path.join(seedDir, "mapping.json"), "{}\n");
  fs.writeFileSync(path.join(seedDir, "NOTICE.md"), "# Synthetic PostgreSQL seed\n");
  const registryHash = sha256(fs.readFileSync("tests/eval/fixtures/datasets.json"));
  fs.writeFileSync(path.join(seedDir, "seed.json"), JSON.stringify({
    version: 1, dataset, registry_sha256: registryHash, corpus_rows: 1,
    base_release: "v0.0.0", source_revision: "a".repeat(40), postgres_image: postgresImage, schema_version: 1,
    embedding_model: "text-embedding-3-small", embedding_dimensions: 1536,
    canonical_sha256: "b".repeat(64), search_sha256: "c".repeat(64), mapping_sha256: await hashFile(path.join(seedDir, "mapping.json")),
    scope: { team_id: team, credential_id: team, owner_id: team, space_id: space, generation: 1 },
  }));
  dumpSeed(docker, container, seedDir);
  await sealSeed(seedDir);
  sql("UPDATE retained_evidence SET content='changed after export';");
  resetDatabase();
  restoreSeed(docker, container, seedDir);
  assert.equal(sql("SELECT content FROM retained_evidence WHERE id=1;"), "exact public Evidence");
  assert.equal(sql("SELECT count(*) FROM ontology_maintenance_markers;"), "1");
  assert.equal(sql("SELECT count(*) FROM memory_spaces;"), "1");
  for (const table of excludedSeedData) assert.equal(sql(`SELECT count(*) FROM ${table};`), "0", `${table} retains its schema without payload rows`);
  await verifySeedCredentialExclusions(docker, container, seedDir, [canary]);

  const bootCanary = `seed-boot-password-${randomUUID()}`;
  const image = await buildBootableSeed(docker, container, seedDir, [canary, bootCanary]);
  const packaged = path.join(paths.directory, "packaged");
  extractSeed(docker, image, packaged, `densemem-eval-${paths.runID}-seed`, paths.runID, { bootable: true, allowLocal: true });
  await verifySeedFiles(packaged, dataset, registryHash, 1, { bootable: true });
  assert.equal(fs.existsSync(path.join(packaged, "database.dump")), false);
  const missingPassword = spawnSync("docker", ["run", "--rm", "--network=none", "-e", "POSTGRES_USER=densemem", "-e", "POSTGRES_DB=densemem", image], { encoding: "utf8", timeout: 10_000 });
  assert.equal(missingPassword.status, 1);
  assert.match(missingPassword.stderr, /Invalid seeded PostgreSQL/);

  const bootEnv = path.join(paths.directory, "seeded-postgres.env");
  fs.writeFileSync(bootEnv, `POSTGRES_USER=densemem\nPOSTGRES_DB=densemem\nPOSTGRES_PASSWORD=${bootCanary}\n`, { mode: 0o600 });
  const boot = docker(["run", "--detach", "--rm", "--network=none", "--env-file", bootEnv, image]);
  t.after(() => docker(["stop", "--timeout", "10", boot]));
  const bootDeadline = Date.now() + 60_000;
  while (spawnSync("docker", ["exec", boot, "pg_isready", "--host=127.0.0.1", "-U", "densemem"], { stdio: "ignore" }).status !== 0) {
    assert.ok(Date.now() < bootDeadline, "Preloaded PostgreSQL readiness timed out");
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
  const bootSQL = (statement) => docker(["exec", "--interactive", boot, "sh", "-ec", 'export PGPASSWORD="$POSTGRES_PASSWORD"; exec psql -XAt -v ON_ERROR_STOP=1 -h 127.0.0.1 -U densemem -d densemem'], { input: statement });
  assert.equal(bootSQL("SELECT content FROM retained_evidence WHERE id=1 AND embedding='[1,2,3]'::vector"), "exact public Evidence");
  assert.equal(bootSQL("SELECT count(*) FROM pg_indexes WHERE indexname='retained_evidence_vector'"), "1");
  assert.equal(bootSQL("SELECT count(*) FROM ontology_maintenance_markers"), "1");
  for (const table of excludedSeedData) assert.equal(bootSQL(`SELECT count(*) FROM ${table}`), "0");
  const wrongPassword = spawnSync("docker", ["exec", boot, "sh", "-ec", 'PGPASSWORD=wrong psql -XAt -h 127.0.0.1 -U densemem -d densemem -c "select 1"'], { encoding: "utf8", timeout: 5000 });
  assert.equal(wrongPassword.status, 2);
  assert.match(wrongPassword.stderr, /password authentication failed/);
  const bootLogs = docker(["logs", boot]);
  assert.match(bootLogs, /Skipping initialization/);
  assert.ok(!bootLogs.includes(canary) && !bootLogs.includes(bootCanary));

  for (const file of ["seed.json", "mapping.json"]) {
    const target = path.join(seedDir, file);
    const original = fs.readFileSync(target);
    fs.writeFileSync(target, JSON.stringify({ canary }));
    await assert.rejects(verifySeedCredentialExclusions(docker, container, seedDir, [canary]), /protected runtime configuration/);
    fs.writeFileSync(target, original);
  }
  const contaminated = path.join(paths.directory, "contaminated");
  fs.mkdirSync(contaminated, { mode: 0o700 });
  fs.copyFileSync(path.join(seedDir, "seed.json"), path.join(contaminated, "seed.json"));
  const raw = `protected-credential-${randomUUID()}`;
  const encoded = `serialized credential/${randomUUID()}`;
  sql(`INSERT INTO retained_evidence VALUES (2, '${raw}'), (3, '${Buffer.from(encoded).toString("base64")}');`);
  dumpSeed(docker, container, contaminated);
  resetDatabase();
  restoreSeed(docker, container, contaminated);
  for (const secret of [raw, encoded]) await assert.rejects(verifySeedCredentialExclusions(docker, container, contaminated, [secret]), /protected runtime configuration/);
  assert.equal(fs.existsSync(path.join(contaminated, "verification.sql")), false);

  resetDatabase();
  sql(`CREATE FUNCTION reject_seed_restore() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'restore rejected ${canary}'; END $$;
    CREATE EVENT TRIGGER reject_seed_restore ON ddl_command_start WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION reject_seed_restore();`);
  assert.throws(() => restoreSeed(docker, container, seedDir), /Docker operation exec failed/);
  assert.match(fs.readFileSync(diagnostic, "utf8"), /restore rejected/);
  assert.ok(fs.readFileSync(diagnostic, "utf8").includes(canary));
  assert.equal(fs.statSync(diagnostic).mode & 0o777, 0o600);
  assert.match(protectedDiagnostics(), /restore rejected/);

  fs.writeFileSync(path.join(paths.directory, "seed-failure.log"), JSON.stringify({ message: `seed publication rejected ${canary}`, diagnostic: `${Buffer.from(canary).toString("base64")} ${Buffer.from(canary).toString("hex")} ${Buffer.from(canary).toString("hex").toUpperCase()}` }, null, 2) + "\n", { mode: 0o600 });
  assert.match(protectedDiagnostics(), /seed publication rejected/);

  const broken = path.join(paths.directory, "broken");
  fs.mkdirSync(broken, { mode: 0o700 });
  fs.writeFileSync(path.join(broken, "seed.json"), "{}\n");
  fs.writeFileSync(path.join(broken, "database.dump"), "broken", { mode: 0o600 });
  docker(["cp", path.join(broken, "database.dump"), `${container}:/tmp/evaluation-seed.dump`]);
  await assert.rejects(verifySeedCredentialExclusions(docker, container, broken, [canary]), /Docker operation exec failed/);
  assert.match(fs.readFileSync(path.join(broken, "postgres-failure.log"), "utf8"), /PostgreSQL verification failed[\s\S]*input file does not appear to be a valid archive/);
  assert.equal(fs.existsSync(path.join(broken, "verification.sql")), false);
});
