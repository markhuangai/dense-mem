import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

const atlas = JSON.parse(readFileSync("_data/examples/atlas.json", "utf8"));
const migration = JSON.parse(readFileSync("_data/migration.json", "utf8"));
const fixtureLine = atlas.subject.content.slice(0, atlas.subject.content.indexOf("\n") + 1);
const visibleStep = page => page.locator("article[data-step]:visible");
const payload = async (scope, name) => JSON.parse(await scope.locator('[data-sample="' + name + '"]').first().textContent());

async function navigate(page, path = "./") {
  await page.goto(path);
  await expect(page.locator("[data-enhanced=true]")).toHaveCount(1);
}

test("walks all 17 Atlas steps and preserves named and numbered deep links", async ({page}) => {
  await navigate(page);
  await expect(page.getByRole("heading", {name: "The life of a memory"})).toBeVisible();
  for (const label of ["Quick Start", "All guides", "Search"]) {
    await expect(page.getByRole("navigation", {name: "Documentation"}).getByRole("link", {name: label})).toBeVisible();
  }
  for (let step = 1; step <= 17; step++) {
    await expect(visibleStep(page)).toHaveCount(1);
    await expect(page.locator("[data-counter]")).toHaveText("Step " + step + " of 17");
    if (step < 17) await page.locator("[data-next]").click();
  }
  await page.keyboard.press("ArrowLeft");
  await expect(visibleStep(page)).toHaveId("trace-source");
  await page.locator("[data-back]").click();
  await expect(visibleStep(page)).toHaveId("follow-topic");
  await page.goBack();
  await expect(visibleStep(page)).toHaveId("trace-source");

  await navigate(page, "./#step-8");
  await expect(visibleStep(page)).toHaveId("choose-topic");
  await navigate(page, "./#choose-topic");
  await expect(visibleStep(page)).toHaveId("choose-topic");
  await page.getByRole("button", {name: "Steps", exact: true}).click();
  await page.getByRole("dialog").getByRole("link", {name: "Split the note by meaning"}).click();
  await expect(page.getByRole("dialog")).toBeHidden();
  await expect(visibleStep(page)).toHaveId("split-by-meaning");
});

test("reconstructs exact spans and compares valid and invalid provider responses", async ({page}) => {
  await navigate(page, "./#split-by-meaning");
  const sequence = page.locator('[data-sequence="ai_partition"]');
  await sequence.locator("summary").first().click();
  const request = await payload(sequence, "partitionRequest");
  const response = await payload(sequence, "partitionResponse");
  expect(request.request_id).toBe(response.request_id);
  expect(request.items[0].boundary_text).toContain("⟦" + response.items[0].cut_refs[0] + "⟧");
  await sequence.locator("[data-frame-next]").click();
  await sequence.locator("[data-frame-next]").click();
  await expect(sequence.locator("[data-snapshot-value=spanA]")).toHaveText("[0, " + Array.from(fixtureLine).length + ")");
  await sequence.getByRole("button", {name: "Unknown boundary"}).click();
  await sequence.locator("[data-frame-next]").click();
  const rejected = await payload(sequence, "partitionRejected");
  expect(request.items[0].boundary_text).not.toContain(rejected.items[0].cut_refs[0]);
  await sequence.locator("[data-frame-next]").click();
  await expect(sequence.getByText("Published Evidence")).toBeVisible();

  await navigate(page, "./#trace-source");
  const trace = await payload(page, "tracePartition");
  const exact = Array.from(atlas.subject.content).slice(trace.span_start, trace.span_end).join("");
  expect(exact).toBe(fixtureLine);
  expect(trace.original_content_hash).toBe("sha256:" + createHash("sha256").update(atlas.subject.content).digest("hex"));
  await expect(page.locator(".fingerprint")).toHaveText(trace.original_content_hash.slice(7));
});

test("plays a database commit and rollback with one shared source identity", async ({page}) => {
  await navigate(page, "./#commit-evidence");
  const sequence = page.locator('[data-sequence="database_commit"]');
  await sequence.locator("summary").first().click();
  await sequence.locator("[data-frame-next]").click();
  const lineage = (await sequence.locator("[data-frame]:visible").innerText());
  expect(lineage).toContain("Write the original item and lineage");
  await sequence.locator("[data-frame]:visible .payload-detail summary").click();
  const records = await payload(sequence.locator("[data-frame]:visible"), "writeLineage");
  expect(records.remember_submitted_items.content).toBe(atlas.subject.content);
  expect(records.evidence_occurrences.canonical_fragment_id).toBe(atlas.evidence.E1.id);
  expect(records.evidence_occurrences.content).toBe(fixtureLine);
  expect(records.remember_item_partitions.occurrence_id).toBe(records.evidence_occurrences.occurrence_id);

  await sequence.locator("[data-frame-next]").click();
  const search = await payload(sequence, "writeSearch");
  expect(search.search_documents.source_id).toBe(records.evidence_occurrences.canonical_fragment_id);
  expect(search.remember_attempts.outcome).toBe("completed");

  await sequence.getByRole("button", {name: "Roll back before commit"}).click();
  await sequence.locator("[data-frame-next]").click();
  const failure = sequence.locator("[data-frame]:visible");
  await expect(failure.getByRole("heading")).toHaveText("Fail while writing search state");
  await expect(failure.locator("dd")).toHaveText(["0", "0", "Absent"]);
  await sequence.locator("[data-frame-back]").click();
  await expect(sequence.locator("[data-frame]:visible").getByRole("heading")).toHaveText("Stage rows inside the transaction");
});

test("reveals definitions and contrasts reuse with new definitions", async ({page}) => {
  await navigate(page, "./#choose-topic");
  const term = page.getByRole("button", {name: "Bounded vocabulary"});
  const definition = page.locator("#vocabulary-definition");
  await term.hover();
  await expect(definition).toBeVisible();
  await page.locator("h1").hover();
  await expect(definition).toBeHidden();
  await term.focus();
  await expect(definition).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(definition).toBeHidden();
  const sequence = page.locator('[data-sequence="ai_ontology"]');
  await sequence.locator("summary").first().click();
  const reuseRequest = await payload(sequence, "ontologyReuseRequest");
  const reuseResponse = await payload(sequence, "ontologyReuseResponse");
  expect(reuseResponse.definitions).toEqual([]);
  const reused = reuseRequest.definitions.find(topic => topic.ref === reuseResponse.items[0].definition_ref);
  expect(reused.label).toBe(atlas.topics.retention.name);
  await sequence.getByRole("button", {name: "Propose a missing topic"}).click();
  const newRequest = await payload(sequence, "ontologyNewRequest");
  const newResponse = await payload(sequence, "ontologyNewResponse");
  expect(newRequest.definitions.map(topic => topic.label)).not.toContain(atlas.topics.retention.name);
  expect(newResponse.definitions).toHaveLength(1);
  expect(newResponse.definitions[0].label).toBe(atlas.topics.retention.name);
  expect(newResponse.items.every(item => item.definition_ref === newResponse.definitions[0].ref)).toBe(true);
  await sequence.locator("[data-frame-next]").click();
  await expect(sequence.locator("[data-frame]:visible").getByRole("heading")).toHaveText("Propose and use one new definition");
});

test("calculates hybrid and lexical ranks and applies direct topic membership", async ({page}) => {
  await navigate(page, "./#combine-ranks");
  await expect(visibleStep(page).locator("tbody .score-value")).toHaveText([(2/61).toFixed(5), (1/62).toFixed(5), (1/63).toFixed(5)]);
  await page.getByRole("button", {name: "Words only"}).click();
  await expect(visibleStep(page).locator("tbody .ticket")).toHaveText(["E1"]);
  await expect(visibleStep(page).locator("tbody .score-value")).toHaveText([(1/61).toFixed(5)]);
  await navigate(page, "./#follow-topic");
  await expect(visibleStep(page).locator(".found .ticket")).toHaveText(["E3"]);
  await page.getByRole("button", {name: "Parent only"}).click();
  await expect(visibleStep(page).locator(".found")).toContainText("no directly assigned Evidence");
  await page.getByRole("button", {name: "Both children"}).click();
  await expect(visibleStep(page).locator(".found .ticket")).toHaveText(["E2", "E3"]);
  const call = await payload(visibleStep(page), "follow");
  expect(call.topic_ids).toEqual([atlas.topics.retention.id, atlas.topics.hours.id]);
  expect(call.known_evidence_ids).toEqual([atlas.evidence.E1.id]);
});

test("works on touch, supports reduced motion and exposes the read-all view", async ({browser}) => {
  const context = await browser.newContext({viewport: {width: 390, height: 844}, hasTouch: true, reducedMotion: "reduce"});
  try {
    const page = await context.newPage();
    const notes = [];
    page.on("pageerror", error => notes.push(error.message));
    await page.goto("http://127.0.0.1:4173/dense-mem/#step-8");
    await expect(page.locator("[data-enhanced=true]")).toHaveCount(1);
    await page.getByRole("button", {name: "Bounded vocabulary"}).tap();
    await expect(page.locator("#vocabulary-definition")).toBeVisible();
    await expect(visibleStep(page).locator("[data-replay]")).toBeHidden();
    await page.locator("[data-read-all]").tap();
    await expect(page.locator("article[data-step]:visible")).toHaveCount(17);
    const details = page.locator('[data-sequence="ai_ontology"]');
    await expect(details.locator("[data-frame]:visible")).toHaveCount(6);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
    expect(notes).toEqual([]);
  } finally {
    await context.close();
  }
});

test("replays movement and passes the essential accessibility checks", async ({page}) => {
  await page.emulateMedia({reducedMotion: "no-preference"});
  await navigate(page);
  await visibleStep(page).locator("[data-replay]").click();
  const animating = await visibleStep(page).locator(".scene").evaluate(scene => scene.getAnimations({subtree: true}).length);
  expect(animating).toBeGreaterThan(0);
  await page.emulateMedia({reducedMotion: "reduce"});
  const result = await new AxeBuilder({page}).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
  expect(result.violations).toEqual([]);
});

test("runs all workflow guides and finds configuration anchors from search", async ({page}) => {
  const errors = [];
  const external = [];
  page.on("pageerror", err => errors.push(err.message));
  page.on("request", request => {
    if (!request.url().startsWith("http://127.0.0.1:4173/")) external.push(request.url());
  });
  await page.goto("./guides/");
  const links = await page.locator(".trail-grid a").evaluateAll(items => items.map(a => a.href));
  expect(links).toHaveLength(Object.keys(JSON.parse(readFileSync("_data/workflows.json", "utf8"))).length);
  for (const link of links.filter(url => /\/walkthroughs\//.test(url))) {
    await navigate(page, link);
    await expect(visibleStep(page).locator(".process-node.current")).toHaveCount(1);
    await page.locator("[data-next]").click();
    await expect(page.locator("[data-counter]")).toHaveText("Step 2 of 3");
  }
  await page.goto("./search/");
  await page.getByRole("searchbox").fill("CONTROL_HTTP_ADDR");
  await page.getByRole("button", {name: "Search"}).click();
  const result = page.getByRole("link", {name: "Listeners", exact: true});
  await expect(result).toHaveAttribute("href", "/dense-mem/Configuration/#listeners");
  await result.click();
  await expect(page.locator("#listeners")).toBeVisible();
  await expect(page.getByText(":8090", {exact: true}).first()).toBeVisible();
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test("renders each of the migrated Mermaid diagrams", async ({page}) => {
  test.setTimeout(130_000);
  const failed = [];
  for (const record of Object.values(migration.files).filter(record => record.diagrams)) {
    await page.goto("." + record.path);
    const diagrams = page.locator(".diagram-disclosure");
    await expect(diagrams).toHaveCount(Math.max(record.diagrams, await page.locator(".language-mermaid").count()));
    for (const diagram of await diagrams.all()) {
      await diagram.evaluate(element => { element.open = true; });
    }
    for (const diagram of await diagrams.all()) {
      try {
        await expect(diagram.locator(".diagram-render svg")).toHaveCount(1, {timeout: 5000});
      } catch (error) {
        failed.push(record.path + " " + (await diagram.locator(".diagram-render").textContent()).slice(0, 120));
      }
    }
  }
  expect(failed).toEqual([]);
});

test("renders a readable transcript with scripting off", async ({browser}) => {
  const context = await browser.newContext({javaScriptEnabled: false});
  try {
    const page = await context.newPage();
    await page.goto("http://127.0.0.1:4173/dense-mem/");
    await expect(page.locator("article[data-step]:visible")).toHaveCount(17);
    await expect(page.locator("#choose-topic").getByText(atlas.topics.retention.name, {exact: true})).toBeVisible();
    await expect(page.getByRole("link", {name: "Quick Start"})).toBeVisible();
  } finally {
    await context.close();
  }
});
