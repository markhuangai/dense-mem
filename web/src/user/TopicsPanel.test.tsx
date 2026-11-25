import userEvent from "@testing-library/user-event";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserApi, type UserSession } from "./api";
import { TopicsPanel } from "./TopicsPanel";
import { TopicEditor } from "./TopicEditor";
import type { TopicCoverage, TopicDraft } from "./topic-types";

const session = { team: { id: "team-a" }, membership: { role: "manager", grants: ["read", "write"] } } as UserSession;
const edit = { action: "define" as const, expected_version: 0, kind: "topic" as const, definition: { key: "storage", label: "Storage", description: "Durable storage", aliases: [] } };
const draft: TopicDraft = { id: "draft-a", version: 1, state: "open", base_revision: 1, reason: "Storage correction", edits: [edit], updated_at: "2026-10-11T00:00:00Z" };

const coverage: TopicCoverage = { observed_at: "2026-10-18T12:00:00Z", revision: 1, discovery_complete: false, coverage_complete: false, pending_markers: 0, counts: { eligible: 2, organized: 0, pending: 2, ambiguous: 0, failed: 0 } };

function install(saved: TopicDraft[] = [], mutation?: (url: string, body: Record<string, unknown>) => Response, coverageStatus = coverage) {
  const response = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
  const fetcher = vi.fn(async (url: string, options?: RequestInit) => {
    const body = options?.body ? JSON.parse(options.body as string) : {};
    if (options?.method && options.method !== "GET") return mutation?.(url, body) ?? response(draft);
    if (url.includes("/records")) return response({ records: [], revision: 1 });
    if (url.includes("/status")) return response(coverageStatus);
    if (url.includes("/publications")) return response({ publications: [] });
    if (url.includes("/sources")) return response({ sources: [] });
    if (new URL(url, "http://localhost").pathname.endsWith("/drafts")) return response({ drafts: saved });
    return response(draft);
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
