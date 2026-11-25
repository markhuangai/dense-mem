import userEvent from "@testing-library/user-event";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmationProvider } from "../ui/confirmation";
import { UserApi, type UserSession } from "./api";
import { TopicsPanel } from "./TopicsPanel";
import { TopicEditor } from "./TopicEditor";
import type { TopicCoverage, TopicDraft, TopicRecord, TopicSource } from "./topic-types";

const session = { team: { id: "team-a" }, membership: { role: "manager", grants: ["read", "write"] } } as UserSession;
const edit = { action: "define" as const, expected_version: 0, kind: "topic" as const, definition: { key: "storage", label: "Storage", description: "Durable storage", aliases: [] } };
const draft: TopicDraft = { id: "draft-a", version: 1, state: "open", base_revision: 1, reason: "Storage correction", edits: [edit], updated_at: "2026-10-11T00:00:00Z" };

const coverage: TopicCoverage = { observed_at: "2026-10-18T12:00:00Z", revision: 1, discovery_complete: false, coverage_complete: false, pending_markers: 0, counts: { eligible: 2, organized: 0, pending: 2, ambiguous: 0, failed: 0 } };

function topicResponse(data: unknown) {
  return new Response(JSON.stringify({ data }), { status: 200 });
}

function install(saved: TopicDraft[] = [], mutation?: (url: string, body: Record<string, unknown>) => Response, coverageStatus = coverage) {
  const fetcher = vi.fn(async (url: string, options?: RequestInit) => {
    const body = options?.body ? JSON.parse(options.body as string) : {};
    if (options?.method && options.method !== "GET") return mutation?.(url, body) ?? topicResponse(draft);
    if (url.includes("/records")) return topicResponse({ records: [], revision: 1 });
    if (url.includes("/status")) return topicResponse(coverageStatus);
    if (url.includes("/publications")) return topicResponse({ publications: [] });
    if (url.includes("/sources")) return topicResponse({ sources: [] });
    if (new URL(url, "http://localhost").pathname.endsWith("/drafts")) return topicResponse({ drafts: saved });
    return topicResponse(draft);
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}

describe("TopicsPanel", () => {
  beforeEach(() => { vi.restoreAllMocks(); vi.spyOn(window, "confirm").mockReturnValue(true); });

  it.each([false, true])("shows zero-record coverage without a percentage (discovery complete: %s)", async (complete) => {
    install([], undefined, { ...coverage, discovery_complete: complete, coverage_complete: complete, counts: { eligible: 0, organized: 0, pending: 0, ambiguous: 0, failed: 0 } });
    render(<TopicsPanel api={new UserApi("key")} session={session} />);
    const summary = await screen.findByRole("region", { name: "Team topic coverage" });
    expect(within(summary).queryByRole("progressbar")).not.toBeInTheDocument();
    expect(within(summary).getByText(/no eligible shared records discovered/)).toBeVisible();
    expect(within(summary).queryByText("Discovery is in progress. Counts cover discovered shared records.") !== null).toBe(!complete);
  });

  it("saves a current definition with its effective version and audit reason", async () => {
    const record = { id: "topic-a", kind: "topic", version: 3, edit_version: 5, current: true, definition: edit.definition };
    const sent: Record<string, unknown>[] = [];
    const response = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
    const fetcher = vi.fn(async (url: string, options?: RequestInit) => {
      if (options?.method === "POST") {
        expect(url).toBe("/ui/api/topics/edits");
        sent.push(JSON.parse(options.body as string));
        return response({ id: "publication", revision: 9 });
      }
      if (url.includes("/records")) return response({ records: [record], revision: 8 });
      if (url.includes("/status")) return response(coverage);
      if (url.includes("/publications")) return response({ publications: [] });
      if (url.includes("/sources")) return response({ sources: [] });
      return response({ drafts: [] });
    });
    vi.stubGlobal("fetch", fetcher);
    render(<TopicsPanel api={new UserApi("key")} session={session} />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Storage" }));
    await waitFor(() => expect(screen.getByLabelText("Definition label")).toHaveValue("Storage"));
    fireEvent.change(screen.getByLabelText("Definition label"), { target: { value: "PostgreSQL storage" } });
    fireEvent.click(screen.getByRole("button", { name: "Add topic change" }));
    fireEvent.change(screen.getByLabelText("Audit reason"), { target: { value: "Clarify the database" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Topic changes saved.");
    expect(sent).toEqual([{
      operation_key: expect.any(String),
      reason: "Clarify the database",
      edits: [{ ...edit, record_id: "topic-a", expected_version: 5, definition: { ...edit.definition, label: "PostgreSQL storage" } }],
    }]);
  });

  it("reuses the operation key when a Save response is lost", async () => {
    const keys: unknown[] = [];
    install([], (url, body) => {
      expect(url).toBe("/ui/api/topics/edits");
      keys.push(body.operation_key);
      if (keys.length === 1) throw new Error("Response lost");
      return new Response(JSON.stringify({ data: { id: "publication" } }), { status: 200 });
    });
    render(<TopicsPanel api={new UserApi("key")} session={session} />);
    await selectTab("Edit");
    fillNewTopic();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Response lost");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Topic changes saved.");
    expect(keys).toEqual([expect.any(String), keys[0]]);
  });

  it("requires refresh and review after a conflict", async () => {
    const fetcher = install([], () => new Response(JSON.stringify({ error: { message: "Topic state changed" } }), { status: 409 }));
    render(<TopicsPanel api={new UserApi("key")} session={session} />);
    await selectTab("Edit");
    fillNewTopic();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("button", { name: "Refresh records" });
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Refresh records" }));
    await screen.findByText(/Topic records refreshed/);
    expect(screen.getByLabelText("Audit reason")).toHaveValue("Add storage vocabulary");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(fetcher.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(1);
  });

  it("shows previous drafts as read-only history to a read-only manager", async () => {
    install([draft]);
    render(<TopicsPanel api={new UserApi("key")} session={{ ...session, membership: { ...session.membership, grants: ["read"] } }} />);
    await selectTab("History");
    expect(screen.getByRole("region", { name: "Previous drafts" })).toBeInTheDocument();
    expect(screen.getByText(/Storage correction/)).toBeVisible();
    expect(screen.queryByRole("tab", { name: "Edit" })).not.toBeInTheDocument();
  });

  it("keeps catalog, shared Evidence, and history pagination independent", async () => {
    const storage: TopicRecord = { id: "topic-a", kind: "topic", version: 1, retired: false, current: true, definition: edit.definition };
    const archive = { ...storage, id: "topic-b", definition: { ...edit.definition, key: "archive", label: "Archive" } };
    const source: TopicSource = { kind: "evidence", id: "source-a", version: 2, text: "Primary database source", truncated: true };
    const olderDraft: TopicDraft = {
      ...draft, id: "draft-b", reason: "Earlier grouping",
      edits: [{ action: "group_together", expected_version: 1, members: [{ kind: "evidence", id: source.id, version: source.version }] }],
    };
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const { pathname, searchParams } = new URL(url, "http://localhost");
      const cursor = searchParams.get("cursor");
      switch (pathname) {
        case "/ui/api/topics/records":
          if (searchParams.get("kind") !== "topic") return topicResponse({ records: [storage], revision: 1 });
          return topicResponse(cursor === "catalog-next"
            ? { records: [archive], revision: 1 }
            : { records: [storage], revision: 1, next_cursor: "catalog-next" });
        case "/ui/api/topics/sources":
          if (searchParams.get("kind") !== "evidence") throw new Error("Expected shared Evidence");
          if (searchParams.get("q") !== "database") {
            return topicResponse({ sources: [{ ...source, text: "Unfiltered source" }] });
          }
          return topicResponse(cursor === "source-next"
            ? { sources: [{ ...source, id: "source-b", text: "Additional database source", truncated: false }] }
            : { sources: [source], next_cursor: "source-next" });
        case "/ui/api/topics/publications":
          return topicResponse(searchParams.get("after") === "3"
            ? { publications: [{ id: "publication-b", revision: 2, origin: "manager", reason: "Earlier review" }] }
            : { publications: [{ id: "publication-a", revision: 3, origin: "manager", reason: "Current review" }], next_revision: 3 });
        case "/ui/api/topics/drafts":
          return topicResponse(cursor === "draft-next"
            ? { drafts: [olderDraft] }
            : { drafts: [draft], next_cursor: "draft-next" });
        case "/ui/api/topics/status":
          return topicResponse(coverage);
        default:
          throw new Error("Unexpected Topic request: " + pathname);
      }
    }));
    render(<TopicsPanel api={new UserApi("key")} session={session} />);

    await userEvent.selectOptions(await screen.findByLabelText("Catalog type"), "topic");
    await userEvent.click(await screen.findByRole("button", { name: "Load more catalog records" }));
    expect(await screen.findByText("Archive", { selector: "strong" })).toBeVisible();
    expect(screen.getByText("Storage", { selector: "strong" })).toBeVisible();

    await selectTab("Sources");
    expect(screen.getByText(/Unfiltered source/)).toBeVisible();
    await userEvent.type(screen.getByLabelText("Source text or ID"), "database");
    await userEvent.click(screen.getByRole("button", { name: "Inspect sources" }));
    expect(await screen.findByText(/Primary database source.*excerpt/)).toBeVisible();
    expect(screen.queryByText(/Unfiltered source/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Load more sources" }));
    expect(await screen.findByText("Additional database source")).toBeVisible();
    expect(screen.getByText(/Primary database source/)).toBeVisible();

    await selectTab("History");
    await userEvent.click(screen.getByRole("button", { name: "Load more publications" }));
    expect(await screen.findByRole("button", { name: /Earlier review/ })).toBeVisible();
    expect(screen.getByRole("button", { name: /Current review/ })).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Load more previous drafts" }));
    const drafts = screen.getByRole("region", { name: "Previous drafts" });
    expect(within(drafts).getByText(/Storage correction/)).toBeVisible();
    await userEvent.click(await within(drafts).findByText(/Earlier grouping/));
    expect(within(drafts).getByText("evidence source-a · version 2")).toBeVisible();
  });

  it("protects unfinished edits and lets a manager replace or remove a queued correction", async () => {
    const storage: TopicRecord = { id: "topic-a", kind: "topic", version: 1, retired: false, current: true, definition: edit.definition };
    const archive = { ...storage, id: "topic-b", definition: { ...edit.definition, key: "archive", label: "Archive" } };
    const pages: Record<string, unknown> = {
      "/ui/api/topics/records": { records: [storage, archive], revision: 1 },
      "/ui/api/topics/status": coverage,
      "/ui/api/topics/publications": { publications: [] },
      "/ui/api/topics/sources": { sources: [] },
      "/ui/api/topics/drafts": { drafts: [] },
    };
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      const { pathname } = new URL(url, "http://localhost");
      if (!(pathname in pages)) throw new Error("Unexpected Topic request: " + pathname);
      return topicResponse(pages[pathname]);
    }));
    render(<ConfirmationProvider><TopicsPanel api={new UserApi("key")} session={session} /></ConfirmationProvider>);

    await userEvent.click(await screen.findByRole("button", { name: "Edit Storage" }));
    fireEvent.change(screen.getByLabelText("Definition label"), { target: { value: "Queued Storage" } });
    fireEvent.change(screen.getByLabelText("Definition aliases"), { target: { value: "Database, Archive" } });
    await userEvent.click(screen.getByRole("button", { name: "Add topic change" }));
    expect(screen.getByText(/Queued Storage.*Database, Archive/)).toBeVisible();

    fireEvent.change(screen.getByLabelText("Definition label"), { target: { value: "Revised Storage" } });
    await selectTab("Catalog");
    await userEvent.click(screen.getByRole("button", { name: "Edit Archive" }));
    const confirm = await screen.findByRole("alertdialog", { name: "Discard editor changes?" });
    await userEvent.click(within(confirm).getByRole("button", { name: "Cancel" }));
    await selectTab("Edit");
    expect(screen.getByLabelText("Definition label")).toHaveValue("Revised Storage");
    expect(screen.getByText(/Queued Storage.*Database, Archive/)).toBeVisible();

    await userEvent.click(screen.getByRole("button", { name: "Add topic change" }));
    expect(screen.getByText(/Revised Storage.*Database, Archive/)).toBeVisible();
    expect(screen.queryByText(/Queued Storage.*Database, Archive/)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Audit reason"), { target: { value: "Clarify storage" } });
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Remove correction 1" }));
    expect(screen.queryByText(/Revised Storage.*Database, Archive/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();

    await selectTab("Catalog");
    await userEvent.click(screen.getByRole("button", { name: "New topic" }));
    expect(screen.getByLabelText("Definition key")).toHaveValue("");
  });

  it("keeps historical override records readable in publication details", async () => {
	const response = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
	const records = [
		{ id:"definition-a",kind:"topic",version:1,definition:{key:"a",label:"First classification",description:"A"} },
		{ id:"definition-b",kind:"topic",version:1,definition:{key:"b",label:"Second classification",description:"B"} },
		{ id:"assignment-a",kind:"assignment",version:1,assignment:{source:{kind:"evidence",id:"source-a",version:1},definition_id:"definition-a"} },
		{ id:"assignment-b",kind:"assignment",version:1,assignment:{source:{kind:"evidence",id:"source-b",version:1},definition_id:"definition-b"} },
		{ id:"correction-a",kind:"override",version:1,override:{action:"set_classification",definition_id:"definition-a",members:[{kind:"evidence",id:"source-a",version:1}]} },
		{ id:"correction-b",kind:"override",version:1,override:{action:"pin_definition",target_id:"definition-b"} },
	];
	vi.stubGlobal("fetch",vi.fn(async (url:string) => {
		if(url.includes("/records"))return response({records:records.filter(({kind})=>kind!=="override"),revision:1});
		if(url.includes("/status"))return response(coverage);
		if(url.endsWith("/publications/history-a"))return response({id:"history-a",revision:1,record_views:records});
		if(url.includes("/publications"))return response({publications:[{id:"history-a",revision:1,origin:"manager",reason:"Inspection history"}]});
		if(url.includes("/sources"))return response({sources:[]});return response({drafts:[]});
	}));
	render(<TopicsPanel api={new UserApi("key")} session={session} />);
	await screen.findByText("First classification", { selector: "strong" });
	expect(screen.queryByText("Target: Second classification (definition-b)")).toBeNull();
	await selectTab("History");
	fireEvent.click(screen.getByRole("button",{name:/Inspection history/}));
	await screen.findByText("Classification: First classification (definition-a)");
	expect(screen.getByText("Target: Second classification (definition-b)")).toBeVisible();
	expect(screen.queryByRole("button",{name:"Unpin definition"})).toBeNull();
  });

  it("preserves and clears an organizational topic parent outside the catalog page", () => {
    const onAdd=vi.fn();
    const target={id:"topic-child",kind:"topic" as const,version:3,retired:false,current:true,definition:{key:"child",label:"Child",description:"Organizational topic",parent_id:"outside-page"}};
    render(<TopicEditor records={[]} target={target} disabled={false} onAdd={onAdd}/>);
    expect(screen.getByLabelText("Definition parent")).toHaveValue("outside-page");
    expect(screen.queryByLabelText("Parent meaning")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Definition label"),{target:{value:"Revised"}});
    fireEvent.click(screen.getByRole("button",{name:"Add topic change"}));
    expect(onAdd.mock.calls[0][0].definition).toMatchObject({label:"Revised",parent_id:"outside-page"});
    fireEvent.change(screen.getByLabelText("Definition parent"),{target:{value:""}});
    fireEvent.click(screen.getByRole("button",{name:"Add topic change"}));
    expect(onAdd.mock.calls[1][0].definition).not.toHaveProperty("parent_id");
  });

});

function fillNewTopic() {
  fireEvent.change(screen.getByLabelText("Definition key"), { target: { value: "storage" } });
  fireEvent.change(screen.getByLabelText("Definition label"), { target: { value: "Storage" } });
  fireEvent.change(screen.getByLabelText("Definition description"), { target: { value: "Durable storage" } });
  fireEvent.click(screen.getByRole("button", { name: "Add topic change" }));
  fireEvent.change(screen.getByLabelText("Audit reason"), { target: { value: "Add storage vocabulary" } });
}

async function selectTab(name: string) {
  await userEvent.click(await screen.findByRole("tab", { name }));
}
