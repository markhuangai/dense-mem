import { createDrawings } from "./atlas-graphics.js";

const topicChoices = {
  retention: {label: "Retention", keys: ["retention"]},
  parent: {label: "Parent only", keys: ["operations"]},
  children: {label: "Both children", keys: ["retention", "hours"]}
};
const readModes = {hybrid: "Words + vectors", lexical: "Words only"};
const partitionPolicy = "remember.semantic-partition.v1";

async function digest(value) {
  const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return Array.from(new Uint8Array(hash), byte => byte.toString(16).padStart(2, "0")).join("");
}

function prepareEvidence(fixture) {
  const {subject, topics, recall, rows} = fixture;
  const codePoints = Array.from(subject.content);
  const cut = codePoints.indexOf("\n") + 1;
  if (cut <= 0 || cut === codePoints.length) throw new Error("Atlas needs two source lines");
  const evidence = {
    E1: {...fixture.evidence.E1, text: codePoints.slice(0, cut).join("")},
    E2: {...fixture.evidence.E2, text: codePoints.slice(cut).join("")},
    E3: {...fixture.evidence.E3, text: subject.exception}
  };
  return {subject, topics, recall, rows, codePoints, cut, evidence, topicChoices, readModes};
}

function topicDefinition(topics, key, ref, parentRef) {
  const topic = topics[key];
  return {
    ref, kind: "topic", key: topic.key, label: topic.name, description: topic.description,
    aliases: topic.aliases, parent_ref: parentRef ?? ""
  };
}

function ontologyExchange(data, propose) {
  const {topics, evidence, subject} = data;
  const requestId = propose ? "atlas-topic-proposal" : "atlas-topic-reuse";
  const definitions = [topicDefinition(topics, "operations", "d0"), topicDefinition(topics, "hours", "d1", "d0")];
  if (!propose) definitions.push(topicDefinition(topics, "retention", "d2", "d0"));
  const items = ["E1", "E3"].map((id, index) => ({
    ref: "s" + index, kind: "evidence", text: evidence[id].text, owner_ref: "owner0",
    context: {metadata: "{}", labels: "{}", created_at: subject.timestamp},
    locked_definition_ref: "", preferred_definition_ref: ""
  }));
  const topicRef = propose ? "new-retention" : "d2";
  return {
    request: {
      request_id: requestId, items, definitions,
      pairs: [{ref: "p0", left_ref: "s0", right_ref: "s1", required_relation: ""}]
    },
    response: {
      request_id: requestId, definitions: propose ? [topicDefinition(topics, "retention", topicRef, "d0")] : [],
      items: items.map(item => ({ref: item.ref, status: "classified", definition_ref: topicRef, reason: ""})),
      equivalence: [{ref: "p0", relation: "distinct"}]
    }
  };
}

async function preparePayloads(data) {
  const {subject, evidence, topics, recall, rows, codePoints, cut} = data;
  const originalHash = await digest(subject.content);
  const requestId = "partition:" + rows.ingest;
  const itemId = "item:0";
  const boundaryDigest = await digest([requestId, itemId, subject.content].join("\0"));
  const ref = offset => "p" + boundaryDigest.slice(0, 12) + "_" + offset.toString(36);
  const partitionRequest = {
    request_id: requestId, policy_version: partitionPolicy,
    items: [{
      item_id: itemId, boundary_text: evidence.E1.text + "⟦" + ref(cut) + "⟧" + evidence.E2.text,
      start_ref: ref(0), end_ref: ref(codePoints.length)
    }], review_security: true
  };
  const partitionResponse = {
    request_id: requestId,
    items: [{item_id: itemId, cut_refs: [ref(cut)]}],
    evidence_security_results: [{evidence_id: itemId, decision: "pass", signals: []}]
  };
  const assessmentCandidate = {
    request_id: rows.ingest,
    evidence_equivalence_candidates: [
      {evidence_id: "evidence:0", candidates: [{evidence_id: evidence.E3.id, content: evidence.E3.text}]},
      {evidence_id: "evidence:1", candidates: []}
    ]
  };
  const assessmentResponse = {
    request_id: assessmentCandidate.request_id,
    evidence_security_results: [],
    evidence_equivalence_results: assessmentCandidate.evidence_equivalence_candidates.map(group => ({
      evidence_id: group.evidence_id, action: "new", candidate_evidence_id: null
    })),
    evidence_conflict_results: []
  };
  const {request: ontologyReuseRequest, response: ontologyReuseResponse} = ontologyExchange(data, false);
  const {request: ontologyNewRequest, response: ontologyNewResponse} = ontologyExchange(data, true);
  const partition = {
    source_item_id: rows.submittedItem, partition_index: 0, span_start: 0, span_end: cut
  };
  const document = {search_document_id: rows.searchDocument, source_kind: "evidence", source_id: evidence.E1.id, search_state: "current"};
  return {
    originalHash,
    values: {
      evidenceOne: evidence.E1.id, occurrence: rows.occurrence, retentionTopic: topics.retention.name,
      evidenceText: evidence.E1.text, retentionCount: topics.retention.members.length,
      hoursTopic: topics.hours.name, parentTopic: topics.operations.name, submittedItem: rows.submittedItem,
      searchDocument: rows.searchDocument, replayKey: subject.requestKey,
      spanA: "[0, " + cut + ")", spanB: "[" + cut + ", " + codePoints.length + ")"
    },
    samples: {
      remember: {
        target_space: "team_shared", idempotency_key: subject.requestKey,
        evidence: [{content: subject.content, source_type: "manual", source: subject.source}]
      },
      recall: {query: subject.query, ...recall},
      trace: {evidence_id: evidence.E1.id, include_evidence_content: true, include_transitions: true, max_events: 100},
      partitionRequest, partitionResponse,
      partitionRejected: {...partitionResponse, items: [{item_id: itemId, cut_refs: ["illustrative-unknown-reference"]}]},
      assessmentCandidate, assessmentResponse,
      assessmentRejected: {
        ...assessmentResponse, evidence_equivalence_results: [
          {evidence_id: "evidence:0", action: "reuse", candidate_evidence_id: rows.searchDocument},
          assessmentResponse.evidence_equivalence_results[1]
        ]
      },
      ontologyReuseRequest, ontologyReuseResponse, ontologyNewRequest, ontologyNewResponse,
      writeLineage: {
        remember_submitted_items: {source_item_id: rows.submittedItem, ingest_id: rows.ingest, submitted_index: 0, content: subject.content, content_hash: "sha256:" + originalHash},
        evidence_occurrences: {occurrence_id: rows.occurrence, canonical_fragment_id: evidence.E1.id, content: evidence.E1.text},
        remember_item_partitions: {...partition, occurrence_id: rows.occurrence}
      },
      writeSearch: {
        search_documents: document,
        remember_attempts: {attempt_id: rows.ingest, idempotency_key: subject.requestKey, outcome: "completed", evidence_count: 2}
      },
      organizationAssignment: {
        kind: "assignment", version: 1, assignment: {
          source: {kind: "evidence", id: evidence.E1.id, version: 1}, definition_id: topics.retention.id
        }
      },
      embeddingInput: {input: subject.query},
      readCandidate: document,
      recallResult: {evidence_id: evidence.E1.id, context: evidence.E1.text, space_kind: "team_shared", equivalent_evidence_ids: []},
      tracePartition: {...partition, submitted_index: 0, original_content_hash: "sha256:" + originalHash, policy_version: partitionPolicy}
    }
  };
}

export async function loadAtlas(root, announce, animate) {
  const fixture = JSON.parse(document.getElementById("atlas-example").textContent);
  const data = prepareEvidence(fixture);
  const prepared = await preparePayloads(data);
  const state = {readMode: "hybrid", topicChoice: "retention", originalHash: prepared.originalHash};
  const {drawings, fusionRows, followResults} = createDrawings(fixture, data, state);

  function followRequest() {
    return {
      topic_ids: topicChoices[state.topicChoice].keys.map(key => data.topics[key].id),
      known_evidence_ids: [data.evidence.E1.id],
      limit: 5
    };
  }

  function fillSamples(group) {
    for (const sample of group.querySelectorAll("[data-sample]")) {
      const value = sample.dataset.sample === "follow" ? followRequest() : prepared.samples[sample.dataset.sample];
      if (value !== undefined) sample.textContent = JSON.stringify(value, null, 2);
    }
    for (const item of group.querySelectorAll("[data-snapshot-value]")) {
      const value = prepared.values[item.dataset.snapshotValue];
      if (value !== undefined) item.textContent = value;
    }
  }

  function draw(scene) {
    scene.innerHTML = drawings[scene.dataset.scene]();
  }

  root.addEventListener("click", event => {
    const button = event.target.closest("[data-choice]");
    if (!button || button.getAttribute("aria-pressed") === "true") return;
    const group = button.dataset.choice;
    const value = button.dataset.value;
    if (group === "rank") state.readMode = value;
    else state.topicChoice = value;
    const scene = button.closest("[data-scene]");
    draw(scene);
    fillSamples(scene.closest("[data-step]"));
    scene.querySelector('[data-value="' + value + '"]').focus({preventScroll: true});
    animate(scene);
    announce(group === "rank"
      ? readModes[value] + ". " + fusionRows().map(row => row.id + " " + row.score.toFixed(5)).join(", ")
      : topicChoices[value].label + ". " + (followResults().join(", ") || "No results") + ".");
  });

  for (const scene of root.querySelectorAll("[data-scene]")) draw(scene);
  fillSamples(root);
}
