export function createDrawings(fixture, data, example) {
  const { subject, evidence, topics, codePoints, cut, topicChoices, readModes } = data;
  function escapeText(value) {
    return String(value).replace(/[&<>"']/g, ch => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[ch]);
  }
  const mini = text => '<span class="mini-label">' + escapeText(text) + '</span>';
  const datum = text => '<span class="datum">' + escapeText(text) + '</span>';
  const wire = '<span class="down" data-wire aria-hidden="true"></span>';
  function ticket(id, key = id) {
    return '<span class="ticket' + (id === "B" || id === "E2" ? ' quiet' : '') + '" data-track="' + key + '">' + id + '</span>';
  }
  function exactLine(id, showNewline = false) {
    const text = evidence[id].text;
    if (showNewline && text.endsWith("\n")) {
      return escapeText(text.slice(0, -1)) + '<span class="end-mark" aria-label="newline">↵</span>';
    }
    return escapeText(text.trimEnd());
  }
  function line(label, value) {
    return '<div class="record-line"><dt>' + escapeText(label) + '</dt><dd>' + value + '</dd></div>';
  }
  function note(body, footer) {
    return '<div class="sheet" data-move><div class="paper-head"><b>' + escapeText(subject.source) + '</b>' + datum("Atlas team") + '</div>' + body +
      '<div class="paper-foot">' + footer + '</div></div>';
  }
  function noteLines(mark = false) {
    const first = mark
      ? '<mark class="highlight">' + exactLine("E1", true) + '</mark>'
      : exactLine("E1").replace(/\d+ days/, duration => '<mark class="highlight keep-words">' + duration + '</mark>');
    return '<div class="note-text"><span data-track="text-A">' + first + '</span><span data-track="text-B">' + exactLine("E2") + '</span></div>';
  }
  function routeTo(label) {
    return '<div class="route">' + wire + '<span class="capsule" data-move>' + escapeText(label) + '</span></div>';
  }
  function comparisonCard(id, label, quote, trackKey = id) {
    return '<div class="node" data-move><div class="method">' + ticket(id, trackKey) + mini(label) +
      '</div><div class="body-text quoted">' + quote + '</div></div>';
  }
  function membershipTopic(key, showMembers) {
    const topic = topics[key];
    return '<div class="branch" data-move>' + mini("Topic") + '<div class="topic-name">' + escapeText(topic.name) + '</div>' +
      (showMembers ? '<div class="method">' + topic.members.map(id => ticket(id)).join("") + '</div>' : '') + '</div>';
  }
  function modeControls(group, choices, selected) {
    return '<div class="mini-controls" role="group" aria-label="' + (group === "rank" ? "Search channels" : "Example topic filter") + '">' +
      Object.entries(choices).map(([key, label]) => '<button type="button" data-choice="' + group + '" data-value="' + key + '" aria-pressed="' +
        (selected === key) + '">' + escapeText(label) + '</button>').join("") + '</div>';
  }
  const textOrder = fixture.exemplars.text_order;
  const vectorOrder = fixture.exemplars.vector_order;
  function branchList(ids, prefix) {
    return ids.map((id, i) => '<span class="rank-pair"><span class="rank-num">#' + (i + 1) + '</span>' + ticket(id, prefix + '-' + id) + '</span>').join("");
  }
  function fusionRows() {
    const vectors = example.readMode === "hybrid" ? vectorOrder : [];
    const pool = Array.from(new Set([...textOrder, ...vectors]));
    return pool.map(id => {
      const word = textOrder.indexOf(id) + 1;
      const vector = vectors.indexOf(id) + 1;
      const score = (word ? 1 / (60 + word) : 0) + (vector ? 1 / (60 + vector) : 0);
      return { id, word, vector, score };
    }).sort((a, b) => b.score - a.score || a.id.localeCompare(b.id));
  }
  function followResults() {
    const union = new Set(topicChoices[example.topicChoice].keys.flatMap(key => topics[key].members));
    return Array.from(union).filter(id => id !== "E1").sort();
  }
  const drawings = {
    submit() {
      return '<div class="stack">' + note(noteLines(), datum("Manual source") + datum("1 item · 2 lines")) + routeTo("remember") + '</div>';
    },
    admit() {
      return '<div class="stack"><div class="sheet" data-move><div class="paper-head"><b>Incoming request</b>' + datum("remember") + '</div><dl>' +
        line("Authenticated writer", "Atlas engineer") + line("Destination", '<code>team_shared</code>') +
        line("Operation key", '<code>' + escapeText(subject.requestKey) + '</code>') + '</dl></div>' + routeTo("New key · continue") + '</div>';
    },
    partition() {
      return '<div class="stack split">' + note('<div class="note-text"><span data-track="text-A">' + exactLine("E1", true) +
        '</span><span data-track="text-B">' + exactLine("E2") + '</span></div>', datum("Cut after the newline") + datum(codePoints.length + " code points")) +
        '<div class="route">' + wire + '</div><div class="pair"><div class="node" data-move><div class="method">' +
        ticket("A") + datum("[0, " + cut + ")") + '</div><p class="body-text">Retention period</p></div><div class="node" data-move><div class="method">' +
        ticket("B") + datum("[" + cut + ", " + codePoints.length + ")") + '</div><p class="body-text">Support hours</p></div></div></div>';
    },
    duplicates() {
      return '<div class="stack"><div class="pair">' + comparisonCard("A", "Submitted slice", exactLine("E1")) +
        comparisonCard("E3", "Already stored", exactLine("E3")) + '</div>' + routeTo("Compare meanings") +
        '<div class="flow-foot">Duration of retention · exception to expiry</div></div>';
    },
    assess() {
      return '<div class="stack"><div class="sheet" data-move><div class="paper-head"><b>Validated assessment</b>' + mini("Example") + '</div><dl>' +
        line("Retention period", ticket("A") + ' &nbsp; new Evidence') + line("Desk opening time", ticket("B") + ' &nbsp; new Evidence') +
        line("Conflict with E3", 'None in this case') + '</dl></div>' + routeTo("Complete response accepted") + '</div>';
    },
    commit() {
      return '<div class="stack"><div class="receipt" data-move><div class="receipt-head"><b>PostgreSQL</b>' + mini("Single transaction") +
        '</div><div class="inside"><div class="pair"><div class="slice-line">' + ticket("E1", "A") + '<p class="body-text">Retention period</p></div>' +
        '<div class="slice-line">' + ticket("E2", "B") + '<p class="body-text">Support hours</p></div></div></div><dl>' +
        line("Provenance", "Original item + two spans") + line("Search", "Text + required vectors") + line("Outcome", "Decisions + replay receipt") +
        '</dl></div>' + routeTo("completed") + '</div>';
    },
    discover() {
      return '<div class="stack"><div class="lane" data-move><div class="label-rule">' + mini("Current team-shared space") +
        '</div><div class="ranks">' + ["E1", "E2", "E3"].map(id => '<span class="rank-pair">' + ticket(id) + ' ' + evidence[id].short + '</span>').join("") +
        '</div></div>' + routeTo("Eligible for shared organization") +
        '<div class="lane disallowed" data-move><div class="label-rule">' + mini("Private space") + '</div><p class="body-text">Private session excerpts stay here.</p></div></div>';
    },
    classify() {
      return '<div class="stack"><div class="sheet" data-move><div class="paper-head"><b>Catalog vocabulary</b>' + mini("Available topics") +
        '</div><div class="topic-def"><b>' + escapeText(topics.retention.name) + '</b><p class="paper-sub">' + escapeText(topics.retention.description) + '</p></div>' +
        '<div class="topic-def"><b>' + escapeText(topics.hours.name) + '</b><p class="paper-sub">' + escapeText(topics.hours.description) + '</p></div></div>' +
        '<div class="route">' + wire + '</div><div class="scope-line" data-move>' + ticket("E1") +
        ' <span aria-hidden="true">→</span> <b>' + escapeText(topics.retention.name) + '</b></div></div>';
    },
    compare() {
      return '<div class="stack"><div class="pair">' + comparisonCard("E1", "Normal expiry", exactLine("E1")) +
        comparisonCard("E3", "Exception", exactLine("E3")) + '</div>' + routeTo("Pair decision · distinct") +
        '<div class="flow-foot">Shared topic · two independent Evidence IDs</div></div>';
    },
    publish() {
      const sources = ["E1", "E2", "E3"].map(id => ticket(id, "source-" + id)).join("");
      return '<div class="stack"><div class="flow-foot">' + mini("Eligible Evidence") + sources + '</div><div class="tree"><div class="node root-topic" data-move>' + escapeText(topics.operations.name) + '</div><div class="fork" data-wire aria-hidden="true"></div><div class="pair">' +
        membershipTopic("retention", true) + membershipTopic("hours", true) + '</div></div></div>';
    },
    ask() {
      return '<div class="stack"><div class="sheet" data-move><div class="paper-head"><b>Teammate’s question</b></div>' +
        '<blockquote class="return-quote">How long does Atlas keep routine backups?</blockquote></div><div class="route">' + wire +
        '</div><div class="node" data-move><div class="method">' + mini("recall_memory") + '</div><div class="record-line"><b>“' +
        subject.query + '”</b>' + datum("limit " + fixture.recall.limit + " · topic_limit " + fixture.recall.topic_limit) + '</div></div></div>';
    },
    channels() {
      return '<div class="stack"><div class="flow-foot"><span class="capsule" data-move>' + escapeText(subject.query) + '</span></div><div class="route">' + wire +
        '</div><div class="lane" data-move><div class="label-rule">' + mini("Full-text order") + '</div><div class="ranks">' + branchList(textOrder, "words") +
        '</div></div><div class="lane" data-move><div class="label-rule">' + mini("Illustrative vector order") + '</div><div class="ranks">' +
        branchList(vectorOrder, "vectors") + '</div></div></div>';
    },
    rank() {
      const rows = fusionRows().map(item => '<tr><td>' + ticket(item.id, (item.word ? "words-" : "vectors-") + item.id) +
        '</td><td>' + (item.word ? '#' + item.word : '—') + '</td><td>' + (item.vector ? '#' + item.vector : '—') +
        '</td><td class="score"><span class="score-bar" aria-hidden="true" style="--score-width:' + (100 * item.score / (2 / 61)) +
        '%"></span><span class="score-value">' + item.score.toFixed(5) + '</span></td></tr>').join("");
      return '<div class="stack">' + modeControls("rank", readModes, example.readMode) + '<div class="rank-sheet" data-move><table>' +
        '<caption class="sr-only">Illustrative ranked Evidence within the shared space</caption><thead><tr><th scope="col">Evidence</th><th scope="col">Text</th><th scope="col">Vector</th><th scope="col">Fused score</th></tr></thead><tbody>' + rows +
        '</tbody></table><p class="table-note">' + (example.readMode === "hybrid" ? 'E1 gains a contribution from both lists.' :
          'Query vector unavailable. E1 survives through full-text search.') + '</p></div></div>';
    },
    return() {
      return '<div class="sheet" data-move><div class="paper-head"><div class="method">' + ticket("E1") + '<b>Returned Evidence</b></div>' +
        datum("team_shared") + '</div><blockquote class="return-quote verbatim" data-track="text-A">' + exactLine("E1", true) +
        '</blockquote><div class="hint"><div><div>' + mini("Related topic") + '</div><b>' + escapeText(topics.retention.name) + '</b></div>' + datum(topics.retention.members.length + " eligible members") + '</div></div>';
    },
    follow() {
      const labels = Object.fromEntries(Object.entries(topicChoices).map(([id, value]) => [id, value.label]));
      const selected = topicChoices[example.topicChoice].keys.map(key => topics[key].name).join(" + ");
      const results = followResults();
      const list = results.map(id => '<p>' + ticket(id) + '<span>' + exactLine(id) + '</span></p>').join("");
      return '<div class="stack">' + modeControls("topic", labels, example.topicChoice) + '<div class="receipt" data-move><div class="receipt-head"><b>' +
        selected + '</b>' + datum("Known Evidence · E1") + '</div><div class="inside found" data-move>' +
        (list || '<p>' + escapeText(topics.operations.name) + ' has no directly assigned Evidence.</p>') + '</div><div class="paper-foot">' +
        datum(results.length + (results.length === 1 ? " result" : " results")) + datum("Direct members only") + '</div></div></div>';
    },
    trace() {
      return '<div class="stack"><div class="flow-foot">' + ticket("E1") + '<span>→ its occurrence → item 0</span></div><div class="route">' + wire + '</div>' +
        note(noteLines(true), datum("Partition 0") + datum("Code points [0, " + cut + ")")) + '<div class="node" data-move>' + mini("Original item · SHA-256") +
        '<p class="fingerprint">' + example.originalHash + '</p></div></div>';
    },
    answer() {
      return '<div class="stack"><div class="answer" data-move>' + mini("Assistant’s answer") +
        '<p>' + exactLine("E1") + ' ' + exactLine("E3") + '</p></div><div class="route">' + wire +
        '</div><div class="pair">' + comparisonCard("E1", "Duration", exactLine("E1")) + comparisonCard("E3", "Exception", exactLine("E3")) + '</div></div>';
    }
  };
  return {drawings, fusionRows, followResults};
}
