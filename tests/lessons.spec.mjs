import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

const activeFrame = page => page.locator("[data-frame]:visible");
const activeView = page => activeFrame(page).locator("[data-view]:visible");
const mode = (page, name) => page.getByRole("navigation", {name: "Lesson view"}).getByRole("link", {name, exact: true});

async function ready(page, route) {
  await page.goto(route);
  await expect(page.locator("[data-ready=true]")).toHaveCount(1);
}

test("opens a story or the full system from the homepage", async ({page}) => {
  await page.goto("./");
  await expect(page.getByRole("heading", {name: "Where’s band practice?"})).toBeVisible();
  const photograph = page.getByRole("img", {name: "Three bandmates playing guitar, bass, and drums in a rehearsal room."});
  await expect.poll(() => photograph.evaluate(img => img.naturalWidth)).toBeGreaterThan(0);
  await page.getByText("Show original", {exact: true}).click();
  await expect(page.locator(".preview-source mark")).toHaveText("We rehearse in Room 4.");
  await page.getByRole("link", {name: "Start with a story"}).click();
  await expect(activeFrame(page)).toHaveId("save");
  await page.goBack();
  await page.getByRole("link", {name: "Explore the system"}).click();
  for (const title of ["Assistants and integrations", "User Portal", "Control Portal", "Inside the backend", "Background workers", "Running and verifying Dense-Mem"]) {
    await expect(page.getByRole("heading", {name: title, exact: true})).toBeVisible();
  }
});

test("plays the same lesson in all three views and follows exact values", async ({page}) => {
  await ready(page, "./learn/rehearsal/");
  await mode(page, "How it works").press("Enter");
  await page.getByRole("button", {name: "Next"}).click();
  await expect(activeFrame(page)).toHaveId("split");
  await expect(activeView(page).locator("[data-diagram=split] [data-piece]")).toHaveCount(3);
  await mode(page, "Follow the data").click();
  await expect(activeFrame(page)).toHaveId("split");
  await expect(activeView(page).locator("details[open] summary")).toContainText("span_end");
  await expect(activeView(page).locator(".data-quote")).toHaveClass(/is-highlighted/);
  const end = Number(await activeView(page).locator("[data-property=end-offset] [data-field-transfer]").textContent());
  await activeView(page).getByText("evidence[0].content", {exact: true}).click();
  const original = JSON.parse(await activeView(page).locator("[data-property=original-content] [data-field-transfer]").textContent());
  expect(Array.from(original).slice(0, end).join("")).toBe("We rehearse in Room 4.\n");
  await expect(page).toHaveURL(/view=data&field=original-content#split$/);
  await page.getByRole("button", {name: "Next"}).click();
  await activeView(page).getByText("results[0].evidence_id", {exact: true}).click();
  const id = await activeView(page).locator("[data-property=recall-id] [data-field-transfer]").textContent();
  const schema = activeView(page).getByRole("link", {name: "recall_memory result"});
  await expect(schema).toHaveAttribute("href", /\/fields\/#recall_memory-result-results-evidence-id$/);
  await page.getByRole("button", {name: "Next"}).click();
  await expect(activeView(page).locator("[data-property=trace-id] [data-field-transfer]")).toHaveText(id);
  await page.goBack();
  await expect(activeFrame(page)).toHaveId("answer");
  await expect(activeView(page).locator("details[open][data-property=recall-id]")).toBeVisible();
  await mode(page, "What happens").click();
  await activeView(page).getByRole("link", {name: "Show original"}).click();
  await expect(activeView(page).locator("mark")).toHaveText("We rehearse in Room 4.");
  await expect(page.getByRole("heading", {name: "Show the original"})).toBeFocused();
  await page.getByRole("button", {name: "Read all frames"}).press("Enter");
  await expect(activeFrame(page)).toHaveCount(4);
  await page.getByRole("button", {name: "Return to playback"}).press("Enter");
  await expect(activeFrame(page)).toHaveId("show-source");
});

test("previews a field and the source quote without changing the lesson", async ({page}) => {
  await ready(page, "./learn/rehearsal/?view=data#split");
  const preview = page.locator("[data-detail-preview]");
  const field = activeView(page).locator("[data-property=start-offset]");
  const summary = field.locator("summary");
  const address = page.url();
  await expect(summary).toHaveAccessibleDescription(/starts at Unicode code-point offset zero/);
  await summary.hover();
  await expect(preview).toBeVisible();
  await expect(preview).toContainText("starts at Unicode code-point offset zero");
  await expect(field).not.toHaveAttribute("open", "");
  await expect(activeView(page).locator("details[open][data-property=end-offset]")).toBeVisible();
  await expect(page).toHaveURL(address);
  await preview.hover();
  await expect(preview).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(preview).toBeHidden();

  await page.mouse.move(1, 1);
  await summary.focus();
  await expect(preview).toBeVisible();
  await summary.press("Enter");
  await expect(preview).toBeHidden();
  await expect(field).toHaveAttribute("open", "");
  await expect(page).toHaveURL(/view=data&field=start-offset#split$/);

  await ready(page, "./learn/rehearsal/#answer");
  const source = activeView(page).getByRole("link", {name: "Show original"});
  await source.hover();
  await expect(preview).toBeVisible();
  await expect(preview).toHaveText("We rehearse in Room 4. Bring headphones.");
  await expect(activeFrame(page)).toHaveId("answer");
  await source.click();
  await expect(preview).toBeHidden();
  await expect(activeFrame(page)).toHaveId("show-source");
});

test("plays on demand, pauses, and moves the split passages from the original words", async ({page}) => {
  await page.emulateMedia({reducedMotion: "no-preference"});
  await page.clock.install();
  await ready(page, "./learn/rehearsal/?view=system");
  await page.getByRole("button", {name: "Play", exact: true}).click();
  await expect(activeFrame(page)).toHaveId("save");
  await page.clock.fastForward(5001);
  await expect(activeFrame(page)).toHaveId("split");
  const keyframes = await activeView(page).locator('[data-piece="room"]').evaluate(piece =>
    piece.getAnimations().flatMap(animation => animation.effect.getKeyframes()).map(frame => frame.transform));
  expect(keyframes.some(transform => transform?.startsWith("translate("))).toBe(true);
  await page.getByRole("button", {name: "Pause", exact: true}).click();
  await page.clock.fastForward(6000);
  await expect(activeFrame(page)).toHaveId("split");
});

test("the paused worker branch stays separate from ordinary search on a phone", async ({browser, baseURL}) => {
  const context = await browser.newContext({baseURL, viewport: {width: 390, height: 844}, hasTouch: true, reducedMotion: "reduce"});
  try {
    const page = await context.newPage();
    await ready(page, "./learn/rehearsal-topic/");
    await page.getByRole("button", {name: "Try with maintenance paused"}).tap();
    await mode(page, "How it works").tap();
    const worker = activeView(page).locator("[data-piece=worker] details");
    await worker.locator("summary").tap();
    await expect(worker).toHaveAttribute("open", "");
    await expect(worker.getByText(/Eligible new shared saves still receive foreground assignments/)).toBeVisible();
    await expect(page.locator("[data-detail-preview]")).toBeHidden();
    await mode(page, "Follow the data").tap();
    await activeView(page).getByText("topic_maintenance_state.paused", {exact: true}).tap();
    await expect(activeView(page).locator("[data-property=maintenance-paused] [data-field-transfer]")).toHaveText("true");
    const bookmark = page.url();
    await page.reload();
    await expect(page).toHaveURL(bookmark);
    await expect(activeView(page).locator("[data-property=maintenance-paused]")).toHaveAttribute("open", "");
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
    expect(await activeFrame(page).evaluate(element => element.getAnimations({subtree: true}).length)).toBe(0);
    await mode(page, "What happens").tap();
    await page.getByRole("button", {name: "Next"}).tap();
    await expect(activeView(page)).toContainText("We rehearse in Room 4.");
    await page.getByRole("button", {name: "Next"}).tap();
    await expect(activeView(page)).toContainText("Assignment still waiting");
  } finally {
    await context.close();
  }
});

test("keeps static frames and original bookmarks usable without scripting", async ({browser, page}) => {
  await page.goto("./#step-8");
  await expect(page).toHaveURL(/\/walkthroughs\/atlas\/#choose-topic$/);
  await expect(page.locator("[data-enhanced=true]")).toHaveCount(1);
  await expect(page.locator("article[data-step]:visible")).toHaveId("choose-topic");
  await page.goto("./#trace-source");
  await expect(page).toHaveURL(/\/walkthroughs\/atlas\/#trace-source$/);
  const context = await browser.newContext({javaScriptEnabled: false});
  try {
    const staticPage = await context.newPage();
    await staticPage.goto("http://127.0.0.1:4173/dense-mem/#step-8");
    await expect(staticPage.locator("#step-8").getByRole("link")).toHaveAttribute("href", /\/walkthroughs\/atlas\/#choose-topic$/);
    await staticPage.goto("http://127.0.0.1:4173/dense-mem/learn/rehearsal/");
    await expect(staticPage.locator("[data-frame]:visible")).toHaveCount(4);
    await expect(staticPage.locator("#split [data-view]:visible")).toHaveCount(3);
    await expect(staticPage.locator("#show-source mark:visible").first()).toHaveText("We rehearse in Room 4.");
  } finally {
    await context.close();
  }
});

test("opens field search results at the matching step and checks accessibility", async ({page}) => {
  await page.goto("./search/?q=topic_maintenance_state.paused");
  await page.locator('.search-results a[href*="scenario=paused"]').first().click();
  await expect(activeView(page).locator("details[open][data-property=maintenance-paused]")).toBeVisible();
  const result = await new AxeBuilder({page}).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
  expect(result.violations).toEqual([]);
});

test("the complete field reference fits on a narrow screen", async ({page}) => {
  await page.setViewportSize({width: 320, height: 800});
  await page.goto("./fields/#submit_recall_session_feedback");
  await expect(page.getByRole("heading", {name: "submit_recall_session_feedback"})).toBeVisible();
  await page.evaluate(() => document.fonts.ready);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(320);
});

test("every listed lesson has working frames and an inspector", async ({page}) => {
  await page.goto("./system/");
  const lessons = await page.locator(".lesson-list a").evaluateAll(links => links.map(link => link.href));
  expect(lessons.length).toBeGreaterThan(0);
  for (const link of lessons) {
    await ready(page, link);
    await expect(activeView(page)).toHaveAttribute("data-view", "story");
    await mode(page, "How it works").click();
    await expect(activeView(page).locator("[data-piece]").first()).toBeVisible();
    await mode(page, "Follow the data").click();
    await expect(activeView(page).locator("details[open] summary").first()).toBeVisible();
  }
});
