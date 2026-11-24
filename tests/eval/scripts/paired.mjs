import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { docker, main as workflow, location, saveRuntimeJSON } from "./workflow.mjs";
import { evaluationDatasets, requireRunner, resolveEvaluationImage } from "../../../.github/scripts/evaluation-policy.mjs";
import { extractSeed, verifySeedFiles } from "../../../.github/scripts/evaluation-seeds.mjs";
import { createHash } from "node:crypto";

const read = file => JSON.parse(fs.readFileSync(file, "utf8"));
const source = "tests/eval/datasets.json";
const datasetList = () => read(source).datasets;
const hash = value => createHash("sha256").update(value).digest("hex");
const digest = /^ghcr\.io\/markhuangai\/dense-mem-eval-postgres@sha256:[0-9a-f]{64}$/;
const application = /^ghcr\.io\/markhuangai\/dense-mem(?::[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$/;
const sha = /^[0-9a-f]{40}$/;

export function pairMatrix(env, registry) {
  const arms = ["baseline", "candidate"];
  const datasets = evaluationDatasets(registry, env.EVAL_DATASET_SELECTION);
  if (env.EVAL_MODE !== "live" || datasets.length !== 5 || registry.datasets.length !== 5) throw new Error("Paired evaluation requires all five live datasets");
  const lock = JSON.parse(env.EVAL_PAIR_LOCK);
  if (Object.keys(lock).length !== datasets.length) throw new Error("Paired seed lock has the wrong dataset count");
  const seen = new Set();
  const prepare = [], score = [];
  for (const { id } of datasets) {
    const row = lock[id];
    if (!row || Object.keys(row).sort().join(",") !== "baseline,candidate,parent") throw new Error(`Seed lock is incomplete for ${id}`);
    for (const ref of Object.values(row)) {
      if (!digest.test(ref) || seen.has(ref)) throw new Error("Expected a distinct immutable public seed digest");
      seen.add(ref);
    }
    for (const arm of arms) {
      const base = arm === "baseline";
      const image = base ? env.EVAL_BASELINE_IMAGE : env.EVAL_IMAGE;
      const revision = base ? env.EVAL_BASE_REVISION : env.EVAL_SOURCE_REVISION;
      if (!application.test(image) || !sha.test(revision) || env.EVAL_IMAGE === env.EVAL_BASELINE_IMAGE) throw new Error("Paired applications require distinct validated images");
      const item = {dataset:id, arm, image, revision, parent:row.parent, seed:row[arm]};
      prepare.push(item);
      for (let shard = 0; shard < 4; shard++) score.push({...item, phase:"score", shard, shards:4});
      if (!base) score.push({...item, phase:"lexical", shard:0, shards:0});
      if (id === "hotpotqa") score.push({...item, phase:"counterevidence", shard:0, shards:0});
    }
  }
  return {prepare,score};
}

function preparationFiles(root) {
  return fs.readdirSync(root,{recursive:true})
    .filter(file => path.basename(file) === "preparation.json")
    .map(file => read(path.join(root,file)));
}

export function verifyPreparations(matrix, receipts, registryHash, registry) {
  if (matrix.length !== 10 || receipts.length !== 10) throw new Error("Scoring requires ten preparation manifests");
  for (const arm of matrix) {
    const matches = receipts.filter(record => record.dataset === arm.dataset && record.arm === arm.arm);
    if (matches.length !== 1) throw new Error(`Missing or duplicate preparation: ${arm.dataset}/${arm.arm}`);
    const record = matches[0];
    const dataset = registry.datasets.find(value => value.id === arm.dataset);
    if (record.version !== 1 || record.parent_seed_image !== arm.parent || record.prepared_seed_image !== arm.seed || record.application_image !== arm.image || record.source_revision !== arm.revision || record.registry_sha256 !== registryHash || record.corpus_rows !== dataset.corpus.rows ||
      ![record.canonical_sha256,record.mapping_sha256,record.search_sha256,record.organization_sha256].every(value => /^[0-9a-f]{64}$/.test(value)) ||
      !Number.isSafeInteger(record.eligible_evidence) || record.eligible_evidence < 1 || record.assigned_evidence !== record.eligible_evidence ||
      !Number.isSafeInteger(record.direct_memberships) || record.direct_memberships < record.assigned_evidence ||
      record.database_work?.net_new_direct_memberships !== record.direct_memberships || record.database_work.net_new_assigned !== record.assigned_evidence ||
      ![record.database_work.sql_statements,record.database_work.written_rows,record.database_work.wall_elapsed_ms_including_resumes].every(value => Number.isSafeInteger(value) && value > 0) ||
      !Number.isSafeInteger(record.topics) || record.topics < 1 || (arm.arm === "candidate" && record.terminal_placements !== record.topics)) throw new Error(`Preparation failed identity or coverage checks for ${arm.dataset}/${arm.arm}`);
    const counterpart = receipts.find(other => other.dataset === arm.dataset && other.arm !== arm.arm);
    if (!counterpart || ["parent_seed_image","canonical_sha256","mapping_sha256","search_sha256","corpus_rows"].some(field => counterpart[field] !== record[field])) throw new Error("Paired arms differ in source or search fingerprints");
  }
}

async function verifyParent(env) {
  const dir = path.join(env.RUNNER_TEMP,`pair-parent-${env.EVAL_DATASET}`);
  const registryHash = hash(fs.readFileSync(source));
  const count = datasetList().find(dataset=>dataset.id===env.EVAL_DATASET).corpus.rows;
  extractSeed(docker,env.EVAL_PARENT_SEED_IMAGE,dir,`pair-parent-${env.GITHUB_RUN_ID}-${env.EVAL_DATASET}`,env.GITHUB_RUN_ID,{bootable:true});
  const parent = await verifySeedFiles(dir,env.EVAL_DATASET,registryHash,count,{bootable:true});
  if (parent.prepared_topics) throw new Error("Original parent must be an unprepared imported seed");
  const out=path.join(location(env).directory,"public","preparation.json");
  const prepared = read(out);
  if (["canonical_sha256","mapping_sha256","search_sha256","corpus_rows"].some(field=>parent[field]!==prepared[field])) throw new Error("Prepared seed lost its original input fingerprints");
}

export async function main(command,env=process.env) {
  requireRunner(env);
  if (command==="matrix") {
    const data=pairMatrix(env,read(source));
    for (const arm of data.prepare.slice(0,2)) {
      docker(["pull","--quiet",arm.image]);
      const pinned=resolveEvaluationImage(arm.image,"markhuangai/dense-mem",JSON.parse(docker(["image","inspect",arm.image]))[0]);
      if (pinned.revision!==arm.revision) throw new Error("Application digest does not match its PR head or main base");
    }
    for (const [key,include] of Object.entries(data)) fs.appendFileSync(env.GITHUB_OUTPUT,`${key}=${JSON.stringify({include})}\n`);
    return;
  }
  if (command==="gate") {
    verifyPreparations(JSON.parse(env.EVAL_PREPARE_MATRIX).include, preparationFiles(path.join(env.RUNNER_TEMP,"pair-preparations")),hash(fs.readFileSync(source)),read(source));
    return;
  }
  if (command==="measure") {
    let inheritedUsage = {};
    if (env.EVAL_RESUME_QUESTIONS) {
      const previousFile=path.join(path.dirname(env.EVAL_RESUME_QUESTIONS),"scores.json");
      const previous=fs.existsSync(previousFile) ? read(previousFile) : null;
      if (previous && (previous.identity?.workflow_run_id!==env.EVAL_RESUME_RUN || previous.identity?.pair_arm!==env.EVAL_PAIR_ARM || previous.identity?.image!==env.EVAL_IMAGE || previous.identity?.seed_image!==env.EVAL_SEED_IMAGE || (previous.identity?.pair_shard??0)!==Number(env.EVAL_PAIR_SHARD))) throw new Error("Selected question checkpoint belongs to another shard");
      if (!previous?.capture_complete) {
        env={...env};
        delete env.EVAL_RESUME_QUESTIONS;
        process.stdout.write("Prior shard lacks complete diagnostic capture; scoring its questions again.\n");
      } else {
        inheritedUsage = {...previous.server_usage_by_workflow_run, [env.EVAL_RESUME_RUN]: previous.server_usage};
      }
    }
    try {
      await workflow("run",env);
    } finally {
      if (env.EVAL_PAIR_PHASE === "score") {
        const file = path.join(location(env).directory, "public", "scores.json");
        if (fs.existsSync(file)) {
          const report = read(file);
          report.server_usage_by_workflow_run = {...inheritedUsage,[env.GITHUB_RUN_ID]:report.server_usage};
          saveRuntimeJSON(file,report);
        }
      }
    }
    if(env.EVAL_PAIR_PHASE==="verify") await verifyParent(env);
    return;
  }
  if (command==="aggregate") {
    try {
      if (env.EVAL_JOBS_RESULT!=="success") throw new Error("Some paired jobs failed or were cancelled");
      return aggregatePair(env);
    } catch (error) {
      const dir=path.join(env.RUNNER_TEMP,"paired-results","aggregate");
      fs.mkdirSync(dir,{recursive:true});
      saveRuntimeJSON(path.join(dir,"comparison.json"),{complete:false,full_public_campaign:false,workflow_run_id:env.GITHUB_RUN_ID,job_result:env.EVAL_JOBS_RESULT,reason:"paired measurements failed completeness or verification"});
      throw error;
    }
  }
  throw new Error("Unsupported paired runner action");
}

function mean(rows, field) {
  return rows.reduce((sum, row) => sum + Number(row[field]), 0) / rows.length;
}

function p95(rows, field) {
  const values = rows.map(row => row[field]).sort((a, b) => a - b);
  if (!values.length || values.some(value => !Number.isFinite(value) || value < 0)) throw new Error("Invalid Recall latency");
  return values[Math.ceil(values.length * 0.95) - 1];
}

export function summarizeAnswers(rows) {
  if (!rows.length) throw new Error("Empty paired policy");
  let citations = 0, valid = 0, uncited = 0;
  for (const row of rows) {
    if (!Number.isSafeInteger(row.citations) || !Number.isSafeInteger(row.valid_citations) ||
        row.valid_citations < 0 || row.citations < row.valid_citations) throw new Error("Invalid paired citation counts");
    citations += row.citations;
    valid += row.valid_citations;
    if (!row.citations) uncited++;
  }
  return {
    answers: rows.length, all_support_at_10: mean(rows, "all_support_retrieved"), recall_at_10: mean(rows, "recall_at_10"),
    em: mean(rows, "exact_match"), f1: mean(rows, "f1"), recall_p95_ms: p95(rows, "initial_recall_ms"),
    exact_citations: valid, submitted_citations: citations, exact_citation_rate: citations ? valid / citations : null,
    answers_without_citations: uncited, mean_cited_gold_source_coverage: mean(rows, "cited_gold_source_coverage")
  };
}

export function summarizeLexical(report, current) {
  const rows = report.scores;
  const ids = new Set(rows.map(row => row.question));
  if (!rows.length || report.expected !== rows.length || ids.size !== rows.length || current.length !== rows.length ||
      current.some(row => !ids.has(row.question_id)) || rows.some(row => !Number.isFinite(row.recall_at_10) || row.recall_at_10 < 0 || row.recall_at_10 > 1) ||
      !report.capture_complete || report.query_embedding_attempts !== 0) throw new Error("Incomplete lexical Recall arm or embedding ablation");
  return {
    questions: rows.length, recall_at_10: mean(rows, "recall_at_10"),
    all_support_at_10: rows.filter(row => row.recall_at_10 === 1).length / rows.length,
    recall_p95_ms: p95(rows, "elapsed_ms"), query_embedding_attempts: 0
  };
}

function aggregatePair(env) {
  const root=path.join(env.RUNNER_TEMP,"paired-results");
  const registry=read(source);
  const jobs=JSON.parse(env.EVAL_SCORE_MATRIX).include;
  const summary=[];
  const cases=new Map();
  const secondary=new Map();
  for(const job of jobs) {
    const suffix=`${job.phase}-${job.dataset}-${job.arm}-${job.shard}`;
    const file=path.join(root,`pair-result-${suffix}`,job.phase==="score"?"scores.json":`${job.phase}.json`);
    if(!fs.existsSync(file)) throw new Error(`Missing paired output ${suffix}`);
    const report=read(file);
    if(job.phase==="score") {
      const size=registry.datasets.find(d=>d.id===job.dataset).questions.rows;
      const count=Math.floor((size+job.shards-1-job.shard)/job.shards);
      const identity=report.identity;
      if(!report.complete||!report.capture_complete||report.expected_answers!==2*count||report.completed_answers!==2*count||identity.dataset!==job.dataset||identity.pair_arm!==job.arm||(identity.pair_shard??0)!==job.shard||identity.seed_image!==job.seed||identity.parent_seed_image!==job.parent||identity.image!==job.image||identity.source_revision!==job.revision||identity.workflow_run_id!==env.GITHUB_RUN_ID) throw new Error(`Incomplete or mismatched score shard ${suffix}`);
      const key = job.dataset + "/" + job.arm;
      const scores = cases.get(key) || new Map();
      cases.set(key,scores);
      for (const result of report.scores) {
        if (!result.completed || result.condition !== "on" || !["single","multi_turn"].includes(result.policy)) throw new Error("Invalid paired answer");
        const unique = result.policy + "/" + result.question_id;
        if (scores.has(unique)) throw new Error("Duplicate paired question");
        scores.set(unique,result);
      }
      summary.push({dataset:job.dataset,arm:job.arm,shard:job.shard,phase:job.phase,expected:report.expected_answers,completed:report.completed_answers,summary:report.comparison_matrix,cost_usd:report.measured_cost_usd,server_usage_by_workflow_run:report.server_usage_by_workflow_run,identity});
    } else {
      if(report.identity?.prepared_seed_image!==job.seed||report.identity.dataset!==job.dataset||report.identity.arm!==job.arm||report.identity.parent_seed_image!==job.parent||report.identity.application_image!==job.image||report.identity.source_revision!==job.revision) throw new Error(`Mismatched supplementary measurement ${suffix}`);
      if(job.phase==="lexical"&&report.scores?.length!==registry.datasets.find(d=>d.id===job.dataset).questions.rows) throw new Error("Incomplete lexical Recall arm");
      if (job.phase==="counterevidence" && (report.complete!==true || report.measurement?.scored_claims!==report.measurement?.expected_claims || report.measurement?.completed_intake!==report.measurement?.expected_intake || !report.measurement?.expected_intake)) throw new Error("Incomplete counterevidence or fresh Remember measurement");
      secondary.set(suffix,report);
      summary.push({phase:job.phase,dataset:job.dataset,arm:job.arm,report});
    }
  }
  fs.mkdirSync(path.join(root,"aggregate"),{recursive:true});
  const deltas = [], lexical = [];
  const overall = [];
  const multi = new Set(["hotpotqa","musique","2wikimultihopqa"]);
  for (const dataset of registry.datasets) {
    const base = cases.get(dataset.id+"/baseline"), current = cases.get(dataset.id+"/candidate");
    if (!base || !current || base.size!==2*dataset.questions.rows || current.size!==base.size || [...base.keys()].some(key=>!current.has(key))) throw new Error("Missing paired question");
    const selected = (rows, policy) => [...rows.values()].filter(row => row.policy === policy);
    for (const policy of ["single","multi_turn"]) {
      const baseline = selected(base, policy), candidate = selected(current, policy);
      if (baseline.length !== dataset.questions.rows || candidate.length !== baseline.length ||
          candidate.some(row => !current.has((policy === "single" ? "multi_turn/" : "single/") + row.question_id))) throw new Error("Mismatched answer policies");
      const before = summarizeAnswers(baseline), after = summarizeAnswers(candidate);
      deltas.push({dataset:dataset.id,policy,baseline:before,candidate:after,
        all_support_delta:after.all_support_at_10-before.all_support_at_10,recall_delta:after.recall_at_10-before.recall_at_10,
        exact_citation_rate_delta:before.exact_citation_rate === null || after.exact_citation_rate === null ? null : after.exact_citation_rate-before.exact_citation_rate});
    }
    const withEmbedding = summarizeAnswers(selected(current, "single"));
    const withoutEmbedding = summarizeLexical(secondary.get(`lexical-${dataset.id}-candidate-0`), selected(current, "single"));
    lexical.push({dataset: dataset.id, candidate_without_query_embedding: withoutEmbedding,
      recall_at_10_delta: withoutEmbedding.recall_at_10 - withEmbedding.recall_at_10,
      all_support_at_10_delta: withoutEmbedding.all_support_at_10 - withEmbedding.all_support_at_10,
      recall_p95_ms_delta: withoutEmbedding.recall_p95_ms - withEmbedding.recall_p95_ms});
  }
  for (const arm of ["baseline","candidate"]) {
    for (const policy of ["single","multi_turn"]) {
      const rows = registry.datasets.flatMap(dataset => [...cases.get(dataset.id+"/"+arm).values()].filter(row => row.policy === policy));
      overall.push({arm,policy,...summarizeAnswers(rows)});
    }
  }
  const twoSides = arm => secondary.get(`counterevidence-hotpotqa-${arm}-0`).measurement;
  const before = twoSides("baseline"), after=twoSides("candidate");
  const supplement = arm => secondary.get(`counterevidence-hotpotqa-${arm}-0`);
  const judgmentsPath = "tests/eval/source_locks/topic_grouping_hotpotqa.json";
  const judgments = read(judgmentsPath), judgmentsHash = hash(fs.readFileSync(judgmentsPath));
  const expectedPairs = judgments.sources.length * (judgments.sources.length - 1) / 2;
  const goldPairs = judgments.sources.reduce((sum,left,i) => sum + judgments.sources.slice(i+1).filter(right => left.labels.some(label=>right.labels.includes(label))).length, 0);
  const grouping = {}, poolWaits = {};
  for (const arm of ["baseline","candidate"]) {
    const {topic_grouping:group, application_pool_waits:pool, identity} = supplement(arm);
    if (group?.judgments_sha256 !== judgmentsHash || group.corpus_sha256 !== judgments.corpus_sha256 ||
        group.sources !== judgments.sources.length || !judgments.sources.every(source => Array.isArray(group.observed_direct_topics_by_source?.[source.source_id])) ||
        group.pairs !== expectedPairs || group.true_positive + group.false_negative !== goldPairs ||
        group.false_positive + group.true_negative !== expectedPairs - goldPairs) throw new Error("Incomplete paired topic judgments");
    if (pool?.source !== "/control/api/metrics" ||
        !(pool.available === false && pool.reason === "application_postgres_pool_not_exposed_by_runtime" && pool.wait_count === null && pool.wait_duration_ns === null ||
          pool.available === true && Number.isSafeInteger(pool.wait_count) && pool.wait_count >= 0 && Number.isSafeInteger(pool.wait_duration_ns) && pool.wait_duration_ns >= 0)) throw new Error("Incomplete application pool observation");
    grouping[arm] = group;
    poolWaits[arm] = {application_image:identity.application_image,...pool};
  }
  poolWaits.comparable = poolWaits.baseline.available && poolWaits.candidate.available;
  const preparations = preparationFiles(path.join(env.RUNNER_TEMP,"pair-preparations"));
  const preparationWork = preparations.map(row => ({
    dataset:row.dataset,arm:row.arm, ...row.database_work,
    sql_per_newly_assigned_evidence:row.database_work.sql_statements/row.database_work.net_new_assigned,
    sql_per_net_new_direct_membership:row.database_work.sql_statements/row.database_work.net_new_direct_memberships
  }));
  if(before.source_sha256!==after.source_sha256 || before.expected_claims!==after.expected_claims || before.expected_intake!==after.expected_intake) throw new Error("Counterevidence comparison inputs differ");
  const multiDelta = deltas.filter(row=>row.policy==="single"&&multi.has(row.dataset)).reduce((sum,row)=>sum+row.all_support_delta,0)/multi.size;
  const singleHold = deltas.filter(row=>row.policy==="single"&&!multi.has(row.dataset)).every(row=>row.recall_delta>=0);
  const qualityGate = multiDelta>0 && after.both_sides_at_10>before.both_sides_at_10 && singleHold;
  const calibration = read(path.join(env.RUNNER_TEMP,"decision-calibration","topic-decision-calibration.json"));
  const holdoutPath = "tests/eval/source_locks/topic_assignment_holdout_v1.json";
  const holdout = read(holdoutPath);
  if (calibration.workflow_run_id !== env.GITHUB_RUN_ID || calibration.source_revision !== jobs.find(job=>job.arm==="candidate").revision ||
      calibration.judgments_sha256 !== hash(fs.readFileSync(holdoutPath)) || calibration.policy_version !== holdout.policy ||
      calibration.expected_cases !== holdout.cases.length || !(calibration.status === "measured" && calibration.completed_cases === calibration.expected_cases ||
        calibration.status === "unavailable" && ["dedicated_decision_credential_missing","dedicated_decision_provider_not_selected"].includes(calibration.reason) &&
        calibration.provider_exchanges === 0 && calibration.clear_set_precision === null && calibration.clear_set_coverage === null)) throw new Error("Invalid Topic Decision calibration receipt");
  let clearSets = 0, exactSets = 0, calls = 0;
  if (calibration.cases.length !== holdout.cases.length) throw new Error("Missing held-out Decision cases");
  for (const [i, label] of holdout.cases.entries()) {
    const observed = calibration.cases[i];
    if (observed.id !== label.id || observed.text !== label.text || observed.uncovered !== label.uncovered ||
        JSON.stringify(observed.expected) !== JSON.stringify(label.expected)) throw new Error("Decision holdout judgments differ");
    if (calibration.status === "unavailable") {
      if (observed.clear !== null || observed.clear_set_exact !== null || observed.provider_exchanges !== null) throw new Error("Unavailable Decision case claimed a measurement");
      continue;
    }
    if (typeof observed.clear !== "boolean" || !observed.provider_exchanges?.length) throw new Error("Missing held-out Decision exchange");
    calls += observed.provider_exchanges.length;
    if (observed.clear) {
      clearSets++;
      const expected = label.expected.map(index=>holdout.topics[index].id).sort();
      const exact = !label.uncovered && JSON.stringify([...observed.matched_topic_ids].sort()) === JSON.stringify(expected);
      if (observed.clear_set_exact !== exact) throw new Error("Incorrect held-out Decision judgment");
      if (exact) exactSets++;
    }
  }
  if (calibration.status === "measured" && (calibration.clear_sets !== clearSets || calibration.exact_clear_sets !== exactSets ||
      calibration.provider_exchanges !== calls || calibration.clear_set_coverage !== clearSets / holdout.cases.length ||
      calibration.clear_set_precision !== (clearSets ? exactSets / clearSets : null))) throw new Error("Decision holdout denominator differs");
  const body = {complete:true,full_public_campaign:true,quality_gate:qualityGate,mean_multihop_all_support_delta:multiDelta,
    both_sides_delta:(after.both_sides_at_10-before.both_sides_at_10)/after.expected_claims,single_hop_no_regression:singleHold,preparation_work:preparationWork,
    measured_cost_usd:null,application_pool_waits:poolWaits,overall,datasets:deltas,query_embedding_ablation:lexical,
    topic_assignment_decision_calibration:calibration,topic_grouping:{scope:judgments.rubric,...grouping},jobs:summary};
  saveRuntimeJSON(path.join(root,"aggregate","comparison.json"),body);
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href===import.meta.url) {
  main(process.argv[2]).catch(error=>{
    const safe=/^(Missing or duplicate preparation:|Preparation failed identity or coverage checks for |Missing paired output |Incomplete or mismatched score shard |Mismatched supplementary measurement )/.test(error.message);
    process.stderr.write((safe ? error.message : "Paired evaluation failed; inspect its protected runtime diagnostics.")+"\n");
    process.exitCode=1;
  });
}
