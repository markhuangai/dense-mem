import { test } from "node:test";
import assert from "node:assert/strict";
import { cohort, score, compare } from "./contextual_intake_v1.mjs";

test("contextual cohort preserves original witnesses and opposing controls", () => {
  const data = cohort();
  assert.equal(data.cases.length, 20);
  assert.equal(new Set(data.cases.map((item) => item.id)).size, 20);
  const quota = data.cases.find((item) => item.id === "quota_original").arguments.evidence[0].content;
  assert.equal(quota.slice(471,484), "output tokens");
  const identifiers = data.cases.find((item) => item.id === "identifiers_original").arguments.evidence[0].content;
  assert.equal(identifiers.slice(345,369), "readSources/currentViews");
  assert.equal(data.cases.filter((item) => item.conflicts === 1).length, 3);
  assert.equal(data.fixture_adaptations.length, 2);
  assert(!JSON.stringify(data.cases).includes("known_entity_id"));
});

test("genuine conflicts require opposing assertions, not unrelated exact substrings", () => {
  for (const id of ["active_contradiction","pending_contradiction","same_observation_contradiction"]) {
    const item=cohort().cases.find((test)=>test.id===id);
    const stored=item.arguments.evidence.map((evidence)=>evidence.content);
    const result={processing_state:"completed",evidence:stored.map((_,index)=>({evidence_index:index,evidence_id:`e${index}`,disposition:"stored"}))};
    const positions=item.opposing_assertions.map((expected)=> {
      const source=stored[expected.evidence_index]; const start=source.indexOf(expected.contains);
      return {evidence_id:`e${expected.evidence_index}`,quote:expected.contains,span_start:start,span_end:start+expected.contains.length};
    });
    assert(score(item,result,stored,[{positions}]).passed);
    const unrelated={positions:positions.map((position)=>({...position,quote:"Atlas",span_start:0,span_end:5}))};
    assert(!score(item,result,stored,[unrelated]).passed);
    const repeated={positions:[positions[0],positions[0]]};
    assert(!score(item,result,stored,[repeated]).passed);
  }
});

test("scoring rejects partial storage, altered text, false and missing conflicts", () => {
  const test = cohort().cases.find((item) => item.id === "quota_accounting");
  const text = test.arguments.evidence[0].content;
  const complete = { processing_state:"completed", evidence:[{disposition:"stored"}] };
  assert(score(test,complete,[text],[]).passed);
  assert(!score(test,complete,[text+" altered"],[]).passed);
  assert(!score(test,complete,[],[]).passed);
  assert(!score(test,complete,[text],[{}]).passed);
  const contradiction = { ...test, conflicts:1 };
  assert(!score(contradiction,complete,[text],[]).passed);
  const rejected = {processing_state:"failed",errors:[{code:"submission_policy_rejected"}],evidence:[{disposition:"not_stored"}]};
  assert(score({...test,accepted:false},rejected,[],[]).passed);
  assert(!score({...test,accepted:false},rejected,[text],[]).passed);
});

test("comparison requires all cases, repetitions, source identities and candidate judgments", () => {
  const report = {cohort_sha256:"a".repeat(64),model:"dense-mem-testing",source_sha:"b".repeat(40),source_state_sha256:"c".repeat(64),complete:true,repetitions:[1,2,3].map((repetition)=>({repetition,samples:cohort().cases.map((test)=>({id:test.id,passed:true}))}))};
  assert(compare(report,structuredClone(report)).passed);
  for (const mutate of [
    (r)=>{r.repetitions.pop();},
    (r)=>{r.repetitions[0].samples[0].passed=false;},
    (r)=>{r.repetitions[0].samples[0].id=r.repetitions[0].samples[1].id;},
    (r)=>{r.cohort_sha256="changed";},
    (r)=>{r.model="substitute";},
    (r)=>{r.source_state_sha256="";},
  ]) { const changed=structuredClone(report); mutate(changed); assert.throws(()=>compare(report,changed)); }
});
