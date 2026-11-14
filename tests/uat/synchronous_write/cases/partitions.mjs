import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { progressCall } from "./progress.mjs";
import { postgresExec, postgresQuery, rememberCanonicalCounts } from "./remember.mjs";
import { captureSemanticAssessmentWindow, reconstructDiagnosticBody } from "../../ontology_assessment_diagnostics.mjs";

export const name = "partitions";

export async function run({ rpc, rawRPC, runStep }) {
  const since = new Date().toISOString();
  const apiKey = process.env.DENSE_MEM_E2E_API_KEY;
  const team = process.env.DENSE_MEM_E2E_TEAM_ID;
  const header = "Atlas preserves its exact source.\r\n\r\n";
  const suffix = " Tail citation Zirconium762 界😀  ";
  const marker = " [fixture-fault:partition-split] ";
  const fill = "Boreal keeps this supporting context together ".repeat(500);
  const content = header + marker + [...fill].slice(0, 16384 - [...header + marker + suffix].length).join("") + suffix;
  assert.equal([...content].length, 16384);
  const argsFor = (text) => ({ target_space: "private", idempotency_key: randomUUID(), evidence: [{ content: text, source_type: "manual", force_insert: true }] });
  let receipt, rollback, diagnosticProof;
  await runStep("long-source-roundtrip-and-replay", async () => {
    receipt = await progressCall({ apiKey, args: argsFor(content), token: JSON.stringify("partition-" + randomUUID()) });
    const result = receipt.terminal.result.structuredContent;
    assert.equal(result.processing_state, "completed", JSON.stringify(result));
    assert.equal(result.evidence.length, 2);
    let reconstructed = "";
    for (const [index, item] of result.evidence.entries()) {
      assert.equal(item.evidence_index, 0);
      assert.equal(item.partition_index, index);
      assert.equal(item.source_item_id, result.evidence[0].source_item_id);
      const traced = (await rpc("tools/call", { name: "trace_memory", arguments: { evidence_id: item.evidence_id, max_fragment_content_runes: 16384 } })).structuredContent;
      assert.equal(traced.evidence.partition.span_start, item.span_start);
      assert.equal(traced.evidence.partition.span_end, item.span_end);
      assert.equal(traced.evidence.partition.original_content_hash, "sha256:" + createHash("sha256").update(content).digest("hex"));
      assert.equal(traced.evidence.content, [...content].slice(item.span_start, item.span_end).join(""));
      reconstructed += traced.evidence.content;
    }
    assert.equal(reconstructed, content);
    const recalled = (await rpc("tools/call", { name: "recall_memory", arguments: { query: "Zirconium762", limit: 20 } })).structuredContent;
    const tail = recalled.results.find((item) => item.evidence_id === result.evidence[1].evidence_id);
    assert(tail, JSON.stringify(recalled));
    assert.equal(tail.context, [...content].slice(result.evidence[1].span_start).join(""));
    assert.equal(tail.context_truncated || false, false);
    const bounded = (await rpc("tools/call", { name: "trace_memory", arguments: { evidence_id: result.evidence[1].evidence_id, max_fragment_content_runes: 32 } })).structuredContent;
    assert.equal(bounded.evidence.content_truncated, true);
    assert(bounded.truncated && bounded.omissions.length > 0);
    const exported = (await rpc("tools/call", { name: "export_memory_pack", arguments: { name: "partition source", include_evidence: true, evidence_ids: result.evidence.map((item) => item.evidence_id) } })).structuredContent;
    const artifact = JSON.parse(exported.artifact_json);
    assert(artifact.evidence.some((item) => item.content.endsWith(suffix)));
    const replay = await progressCall({ apiKey, args: receipt.args, token: "0" });
    assert.deepEqual(replay.terminal.result.structuredContent.evidence, result.evidence);
    assert.equal(replay.progress.at(-1).params.message, "Returning saved result");
  });
  await runStep("unicode-structural-boundaries", async () => {
    for (const separator of ["\u2028", "\u2029"]) {
      const first = "Atlas uses PostgreSQL" + separator;
      const text = first + "Boreal ships Tuesday [fixture-fault:partition-split]";
      const call = await progressCall({ apiKey, args: argsFor(text), token: "0" });
      const result = call.terminal.result.structuredContent;
      assert.equal(result.processing_state, "completed", JSON.stringify(result));
      assert.equal(result.evidence.length, 2);
      assert.equal(result.evidence[0].span_end, [...first].length);
      const slices = [];
      for (const item of result.evidence) {
        const traced = (await rpc("tools/call", { name: "trace_memory", arguments: { evidence_id: item.evidence_id, max_fragment_content_runes: 16384 } })).structuredContent;
        slices.push(traced.evidence.content);
      }
      assert.equal(slices[0], first);
      assert.equal(slices.join(""), text);
    }
  });
  await runStep("code-point-admission-bound", async () => {
    const response = await rawRPC("tools/call", { name: "remember", arguments: argsFor(content + "界") });
    assert.equal(response.error?.code, -32602);
    assert(response.error.data.issues.some((issue) => issue.path === "/evidence/0/content"));
  });
  const rejected = [];
  for (const fault of ["partition-missing", "partition-unknown", "partition-repair"]) {
    await runStep(fault, async () => {
      const call = await progressCall({ apiKey, args: argsFor("Exact partition input. [fixture-fault:" + fault + "]"), token: "0" });
      const result = call.terminal.result.structuredContent;
      if (fault === "partition-repair") {
        assert.equal(result.processing_state, "completed");
        assert(call.progress.some((frame) => frame.params.message === "Regenerating partitions (2/3)"));
      } else {
        assert.equal(result.processing_state, "failed");
        assert.equal(result.errors[0].code, "provider_response_invalid");
        assert.equal(call.progress.filter((frame) => frame.params.message.startsWith("Regenerating partitions")).length, 2);
        assert(rememberCanonicalCounts(team, result.submission_id).every((value) => value === 0));
        assert.equal(Number(postgresQuery(`SELECT count(*) FROM remember_submitted_items WHERE team_id='${team}'::uuid AND ingest_id='${result.submission_id}'::uuid`)), 0);
        rejected.push(call);
      }
    });
  }
  await runStep("publication-rollback-capture", async () => {
    const fn="partition_capture_"+randomUUID().replaceAll("-","");
    const args=argsFor("Intended original.\nIntended tail. [fixture-fault:partition-split]");
    postgresExec(`CREATE FUNCTION ${fn}() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='${args.idempotency_key}' AND NEW.outcome='completed' THEN RAISE EXCEPTION 'partition publication rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER ${fn} BEFORE INSERT ON remember_attempts FOR EACH ROW EXECUTE FUNCTION ${fn}()`, "synchronous-write-partition-setup");
    try {
      rollback=await progressCall({apiKey,args,token:"0"});
      assert.equal(rollback.terminal.result.structuredContent.errors[0].code,"database_failure");
    } finally {postgresExec(`DROP TRIGGER ${fn} ON remember_attempts; DROP FUNCTION ${fn}()`, "synchronous-write-partition-cleanup");}
  });
  await runStep("partition-rejection-from-collected-logs", async () => {
    const logs = captureSemanticAssessmentWindow({ project: process.env.DENSE_MEM_E2E_COMPOSE_PROJECT, composeFile: process.env.DENSE_MEM_E2E_COMPOSE_FILE, since,
      directory: (process.env.DENSE_MEM_E2E_REMEMBER_FAILURE_DIR || "/tmp/dense-mem-remember-failures") + "/partition-debug" });
    assert(!logs.includes(apiKey));
    const records = logs.split(/\r?\n/).filter(Boolean).map(JSON.parse);
    for(const call of [receipt,rollback]) {
      const rows=records.filter((row)=>row.correlation_id===call.correlationID);
      const commit=reconstructDiagnosticBody(rows.filter((row)=>row.msg==="remember_commit_input"));
      assert.equal(commit.SubmittedItems[0].Content,call.args.evidence[0].content);
      const terminal=reconstructDiagnosticBody(rows.filter((row)=>row.msg==="remember_terminal_receipt"));
      assert.equal(terminal.receipt_available,true);
      assert.deepEqual(terminal.receipt.evidence,call.terminal.result.structuredContent.evidence);
      assert.deepEqual(terminal.receipt.errors,call.terminal.result.structuredContent.errors);
      if(call===receipt) {
        for(const [index,item] of terminal.receipt.evidence.entries()) {
          assert.equal(commit.Evidence[index].Partition.source_item_id,item.source_item_id);
          assert.equal(commit.Evidence[index].FragmentID,item.occurrence_id);
          assert.equal(commit.Evidence[index].Partition.span_end,item.span_end);
        }
      } else {
        assert.equal(terminal.outcome,"failed");
        assert(rows.some((row)=>row.msg==="remember_processing_failed" && row.commit_stage==="terminal_attempt"));
        assert.equal(commit.Evidence.length,2);
      }
    }
    for (const call of rejected) {
      const rows = records.filter((row) => row.correlation_id === call.correlationID);
      assert(rows.some((row) => row.msg === "remember_partition_rejected" && row.partition_attempt === 3 && row.validation_reason && row.policy_version && row.model));
      const manifestRows = rows.filter((row) => row.msg === "remember_partition_manifest");
      assert(manifestRows.length > 0, "partition manifest must be present in the actual stdout sink");
      const manifest = reconstructDiagnosticBody(manifestRows);
      assert.equal(manifest.items[0].boundary_text.replace(/⟦[^⟧]+⟧/g, ""), call.args.evidence[0].content);
      assert(rows.some((row) => row.msg === "remember_invocation_completed" && row.outcome && row.canonical_attempt_id));
    }
    const correlations = new Set([receipt, rollback, ...rejected].map((call) => call.correlationID));
    const retained = records.filter((row) => correlations.has(row.correlation_id));
    const exchanges = new Map();
    for (const row of retained) {
      assert.equal(row.team_id, team);
      assert(row.profile_id, "trusted profile must accompany correlated diagnostics");
      if (row.msg !== "ai_exchange_payload") continue;
      const key = `${row.exchange_id}:${row.direction}`;
      const group = exchanges.get(key) || [];
      group.push(row); exchanges.set(key, group);
    }
    for (const correlation of correlations) {
      assert([...exchanges.values()].some((group) => group[0].correlation_id === correlation), "provider exchange capture is missing for an expected correlation");
    }
    for (const group of exchanges.values()) {
      reconstructDiagnosticBody(group);
      assert(exchanges.has(`${group[0].exchange_id}:request`) && exchanges.has(`${group[0].exchange_id}:response`), "provider exchange capture is incomplete");
    }
    const retainedText = retained.map(JSON.stringify).join("\n") + "\n";
    diagnosticProof = { records: retained.length, exchanges: exchanges.size / 2, sha256: createHash("sha256").update(retainedText).digest("hex"), complete: true };
    for (const row of retained) console.log(`remember_partition_diagnostic_record ${JSON.stringify(row)}`);
  });
  return { status: "ok", source_code_points: 16384, partitions: 2, tail_citation: true, malformed_regeneration: true, diagnostics: diagnosticProof };
}
