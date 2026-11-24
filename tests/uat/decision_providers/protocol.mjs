let mode = "normal";
let attempts = 0;
let preferredIDs = [];
const exchanges = [];

export function handleDecisionFixture(request, response, payload, topicAssignment, communityParent) {
  if (request.url === "/decision-fixture") {
    if (request.method === "POST") { mode = payload.mode || "normal"; attempts = 0; preferredIDs = payload.preferred_ids || []; }
    send(response, 200, { mode, exchanges });
    return true;
  }
  const vendor = request.url?.endsWith("/decisions") ? "openai" : request.url?.endsWith("/systemone") ? "typesafe" : "";
  if (!vendor) return false;
  attempts += 1;
  const state = vendor === "openai" ? JSON.parse(payload.input) : payload.state;
  const questions = vendor === "openai" ? payload.questions : Object.entries(payload.questions).map(([name, question]) => ({ ...question, name }));
  exchanges.push({ vendor, mode, question_count: questions.length, names: questions.map((question) => question.name), types: questions.map((question) => question.type) });
  if (mode === "community-partial-outage" && state.community?.topic?.name === "memory-storage" && questions.some((question) => question.name === "parent")) {
    send(response, 529, { error: { message: "community placement fixture outage", code: "fixture_community_unavailable" } });
    return true;
  }
  if (["401", "429", "529"].includes(mode)) { send(response, Number(mode), { error: { message: "decision fixture rejection", code: `fixture_${mode}` } }); return true; }
  if (mode === "malformed" || (mode === "regenerate" && attempts === 1)) { send(response, 200, { model: payload.model, answers: vendor === "openai" ? [] : {} }); return true; }
  const assignment = state.assignment && topicAssignment(state.assignment);
  const answers = questions.map((question) => {
    const base = { name: question.name, type: question.type };
    if (mode === "refusal") return { name: question.name, type: "refusal" };
    if (question.type === "predicate" || question.type === "noul") {
      let probability = mode === "uncertain" ? 0.5 : 0.001;
      if (question.name.startsWith("dedup_")) {
        const [, evidenceIndex, candidateIndex] = question.name.split("_");
        const group = state[`evidence_${evidenceIndex}`];
        probability = normalize(group.submitted_content) === normalize(group.authorized_candidates[Number(candidateIndex)]?.content || "") ? 0.999 : 0.001;
        if (mode === "uncertain") probability = 0.5;
      }
      if (assignment && mode !== "uncertain") {
        if (question.name === "uncovered") probability = assignment.proposals.length ? 0.999 : 0.001;
        if (question.name.startsWith("match_")) {
          const candidate = state.assignment.topics[Number(question.name.slice(6))];
          probability = assignment.matches.some((match) => match.id === candidate.id) ? 0.999 : 0.001;
        }
      }
      if (question.name === "sufficiently_supported") probability = mode === "uncertain" ? 0.5 : 0.999;
      return { ...base, [vendor === "openai" ? "probability" : "noul"]: probability };
    }
    if (question.type === "choice") {
      const choices = vendor === "openai" ? question.choices : Object.entries(question.criteria).map(([value, description]) => ({ value, description }));
      const selected = question.name === "parent" && state.community ? communityParent(state.community) || "standalone" : choices[0].value;
      const probabilities = choices.map((choice) => ({ value: choice.value, probability: choice.value === selected ? 1 : 0 }));
      return { ...base, choice: selected, confidence: 1, probabilities: vendor === "openai" ? probabilities : Object.fromEntries(probabilities.map((item) => [item.value, item.probability])) };
    }
    const levels = vendor === "openai" ? question.levels : question.criteria.map((description) => ({ label: description, description }));
    const top = levels.length - 1;
    const candidate = state[question.name];
    const candidateID = candidate?.evidence?.EvidenceID;
    const score = mode === "ranking" && !preferredIDs.includes(candidateID) ? 0 : top;
    const probabilities = levels.map((level, value) => ({ value, label: level.label, probability: value === score ? 1 : 0 }));
    return { ...base, score, confidence: 1, probabilities: vendor === "openai" ? probabilities : Object.fromEntries(probabilities.map((item) => [String(item.value), item.probability])), ...(vendor === "typesafe" ? { legend: Object.fromEntries(question.criteria.map((description, index) => [String(index), description])) } : {}) };
  });
  const mapped = vendor === "openai" ? answers : Object.fromEntries(answers.map(({ name, ...answer }) => [name, answer]));
  send(response, 200, { model: payload.model, answers: mapped, usage: { input_tokens: 100, output_tokens: 0 } });
  return true;
}

function normalize(text) { return String(text).replaceAll("stores data in", "uses").replaceAll(/\s+/g, " ").trim().toLowerCase(); }
function send(response, status, value) { response.writeHead(status, { "content-type": "application/json" }); response.end(JSON.stringify(value)); }
