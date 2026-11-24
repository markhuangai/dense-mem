import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { pairMatrix, summarizeAnswers, summarizeLexical, verifyPreparations } from "./paired.mjs";

const registry = JSON.parse(readFileSync(new URL("../datasets.json", import.meta.url), "utf8"));
const hex = n => n.toString(16).padStart(64, "0");
const seed = n => "ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:" + hex(n);
const arms = Object.fromEntries(registry.datasets.map(({id}, i) =>
  [id, {parent:seed(i*3+1), baseline:seed(i*3+2), candidate:seed(i*3+3)}]));
const env = {
  EVAL_MODE:"live", EVAL_DATASET_SELECTION:"", EVAL_PAIR_LOCK:JSON.stringify(arms),
  EVAL_IMAGE:"ghcr.io/markhuangai/dense-mem@sha256:"+hex(101),
  EVAL_BASELINE_IMAGE:"ghcr.io/markhuangai/dense-mem@sha256:"+hex(102),
  EVAL_SOURCE_REVISION:"c".repeat(40), EVAL_BASE_REVISION:"b".repeat(40)
};

test("paired preparation rejects a missing or unassigned arm before scoring", () => {
  const {prepare, score} = pairMatrix(env,registry);
  assert.equal(prepare.length,10);
  assert.equal(score.filter(item=>item.phase==="score").length,40);
  assert.equal(score.filter(item=>item.phase==="counterevidence").length,2);
  assert.equal(score.filter(item=>item.phase==="lexical").length,5);
  const receipts = prepare.map(job => ({
    dataset:job.dataset,arm:job.arm,version:1,parent_seed_image:job.parent,prepared_seed_image:job.seed,
    application_image:job.image,source_revision:job.revision,registry_sha256:hex(200),
    canonical_sha256:hex(201),mapping_sha256:hex(202),search_sha256:hex(203),organization_sha256:hex(204),
    corpus_rows:registry.datasets.find(row=>row.id===job.dataset).corpus.rows,
    database_work:{net_new_direct_memberships:10,net_new_assigned:10,sql_statements:50,written_rows:80,wall_elapsed_ms_including_resumes:900},
    eligible_evidence:10,assigned_evidence:10,direct_memberships:10,topics:2,terminal_placements:job.arm==="candidate"?2:0
  }));
  verifyPreparations(prepare,receipts,hex(200),registry);
  assert.throws(()=>verifyPreparations(prepare,receipts.slice(1),hex(200),registry),/ten/);
  const candidate = receipts.find(row=>row.arm==="candidate");
  for (const change of [{assigned_evidence:9},{terminal_placements:1},{canonical_sha256:hex(5)},{parent_seed_image:seed(99)}]) {
    const bad = receipts.map(row=>row===candidate?{...row,...change}:row);
    assert.throws(()=>verifyPreparations(prepare,bad,hex(200),registry));
  }
});

test("paired plan rejects mutable or repeated seed images and a dataset subset", () => {
  const id=registry.datasets[0].id;
  for(const field of [{candidate:arms[id].baseline},{parent:"ghcr.io/markhuangai/dense-mem-eval-postgres:latest"}]){
    const lock={...arms,[id]:{...arms[id],...field}};
    assert.throws(()=>pairMatrix({...env,EVAL_PAIR_LOCK:JSON.stringify(lock)},registry),/distinct immutable/);
  }
  assert.throws(()=>pairMatrix({...env,EVAL_DATASET_SELECTION:JSON.stringify({[id]:true})},registry),/all five/);
});

test("exact citations pool the submitted denominator and retain uncited answers", () => {
  const row = {all_support_retrieved:true, recall_at_10:1, exact_match:0, f1:0, initial_recall_ms:5, cited_gold_source_coverage:0};
  const summary = summarizeAnswers([{...row,citations:1,valid_citations:1}, {...row,citations:3,valid_citations:0}, {...row,citations:0,valid_citations:0}]);
  assert.equal(summary.exact_citation_rate, 0.25);
  assert.equal(summary.submitted_citations, 4);
  assert.equal(summary.answers_without_citations, 1);
  assert.equal(summarizeAnswers([{...row,citations:0,valid_citations:0}]).exact_citation_rate, null);
  assert.throws(() => summarizeAnswers([{...row,citations:0,valid_citations:1}]), /citation counts/);
});

test("lexical ablation requires identical questions and captured zero query embeddings", () => {
  const current = [{question_id:"a"},{question_id:"b"}];
  const report = {expected:2,capture_complete:true,query_embedding_attempts:0,scores:[
    {question:"a",recall_at_10:1,elapsed_ms:1}, {question:"b",recall_at_10:0.5,elapsed_ms:2}
  ]};
  assert.equal(summarizeLexical(report,current).recall_at_10, 0.75);
  assert.throws(() => summarizeLexical({...report,scores:[report.scores[0],report.scores[0]]},current));
  assert.throws(() => summarizeLexical({...report,query_embedding_attempts:1},current));
});
