import userEvent from "@testing-library/user-event";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserApi, type UserSession } from "./api";
import { OntologyPanel } from "./OntologyPanel";
import type { OntologyCoverage, OntologyDraft } from "./ontology-types";

const session = { team: { id: "team-a" }, membership: { role: "manager", grants: ["read", "write"] } } as UserSession;
const edit = { action: "define" as const, expected_version: 0, kind: "topic" as const, definition: { key: "storage", label: "Storage", description: "Durable storage" } };
const draft: OntologyDraft = { id: "draft-a", version: 1, state: "open", base_revision: 1, reason: "Storage correction", edits: [edit], updated_at: "2026-10-11T00:00:00Z" };

const coverage: OntologyCoverage = { observed_at: "2026-10-18T12:00:00Z", revision: 1, discovery_complete: false, coverage_complete: false, pending_markers: 0, counts: { eligible: 2, organized: 0, pending: 2, ambiguous: 0, failed: 0, budget_deferred: 0 } };

function install(saved: OntologyDraft[] = [], mutation?: (url: string, body: Record<string, unknown>) => Response, coverageStatus = coverage) {
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

describe("OntologyPanel", () => {
  beforeEach(() => { vi.restoreAllMocks(); vi.spyOn(window, "confirm").mockReturnValue(true); });

  it.each([false, true])("shows zero-record coverage without a percentage (discovery complete: %s)", async (complete) => {
    install([], undefined, { ...coverage, discovery_complete: complete, coverage_complete: complete, counts: { eligible: 0, organized: 0, pending: 0, ambiguous: 0, failed: 0, budget_deferred: 0 } });
    render(<OntologyPanel api={new UserApi("key")} session={session} />);
    const summary = await screen.findByRole("region", { name: "Team ontology coverage" });
    expect(within(summary).queryByRole("progressbar")).not.toBeInTheDocument();
    expect(within(summary).getByText(/no eligible shared records discovered/)).toBeVisible();
    expect(within(summary).queryByText("Discovery is in progress. Counts cover discovered shared records.") !== null).toBe(!complete);
  });

  it("preserves the selected publication and rollback reason when replacement is cancelled", async () => {
    const publications = [1, 2].map((revision) => ({ id: `publication-${revision}`, revision, origin: "manager", reason: `Review ${revision}`, records: [] }));
    const fetcher = install();
    const fallback = fetcher.getMockImplementation()!;
    fetcher.mockImplementation(async (url, options) => {
      const path = new URL(url, "http://localhost").pathname;
      if (path.endsWith("/publications")) return new Response(JSON.stringify({ data: { publications } }));
      const publication = publications.find((item) => path.endsWith(`/publications/${item.id}`));
      return publication ? new Response(JSON.stringify({ data: publication })) : fallback(url, options);
    });
    render(<OntologyPanel api={new UserApi("key")} session={session} />);
    await selectTab("History");
    await userEvent.click(await screen.findByRole("button", { name: "Revision 1 · manager · Review 1" }));
    fireEvent.change(screen.getByLabelText("Rollback reason"), { target: { value: "Keep my unfinished reason" } });
    vi.mocked(window.confirm).mockReturnValue(false);
    await userEvent.click(screen.getByRole("button", { name: "Revision 2 · manager · Review 2" }));
    expect(window.confirm).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("heading", { name: "Publication revision 1" })).toBeVisible();
    expect(screen.getByLabelText("Rollback reason")).toHaveValue("Keep my unfinished reason");
    expect(fetcher.mock.calls.some(([url]) => url.endsWith("/publications/publication-2"))).toBe(false);
    vi.mocked(window.confirm).mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: "Revision 2 · manager · Review 2" }));
    await screen.findByRole("heading", { name: "Publication revision 2" });
    expect(screen.getByLabelText("Rollback reason")).toHaveValue("");
  });

  it.each([["definition", false], ["definition", true], ["group", false], ["group", true]])(
    "confirms replacing dirty %s editor fields (same target: %s) while retaining the draft",
    async (kind, sameTarget) => {
      const response = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
      const records = [
        { id: "definition-a", kind: "topic", version: 3, definition: { key: "first", label: "First definition", description: "First description" } },
        { id: "definition-b", kind: "topic", version: 5, definition: { key: "second", label: "Second definition", description: "Second description" } },
        { id: "group-a", kind: "evidence_group", version: 2, group: { members: [{ kind: "evidence", id: "source-a", version: 1 }, { kind: "evidence", id: "source-b", version: 2 }] } },
        { id: "group-b", kind: "evidence_group", version: 4, group: { members: [{ kind: "evidence", id: "source-c", version: 3 }, { kind: "evidence", id: "source-d", version: 4 }] } },
      ];
      const fetcher = install([draft], (_url, body) => response({ ...draft, version: 2, reason: body.reason, edits: body.edits }));
      const fallback = fetcher.getMockImplementation()!;
      fetcher.mockImplementation(async (url, options) => url.includes("/records") ? response({ records, revision: 1 }) : fallback(url, options));
      render(<OntologyPanel api={new UserApi("key")} session={session} />);
      await selectTab("Drafts");
      await userEvent.click(await screen.findByRole("button", { name: /Storage correction/ }));
      await screen.findByRole("heading", { name: /Draft draft-a/ });
      async function selectTarget(id: string) {
        await selectTab("Catalog");
        const row = screen.getByTitle(id).closest("tr")!;
        await userEvent.click(within(row).getByRole("button", { name: /^(Edit |Correct grouping$)/ }));
      }
      await selectTarget(`${kind}-a`);
      if (kind === "group") await userEvent.selectOptions(screen.getByLabelText("Correction"), "define");
      fireEvent.change(screen.getByLabelText("Definition label"), { target: { value: "Keep unfinished fields" } });
      fireEvent.change(screen.getByLabelText("Audit reason"), { target: { value: "Keep queued corrections" } });
      vi.mocked(window.confirm).mockReturnValue(false);
      await selectTarget(`${kind}-${sameTarget ? "a" : "b"}`);
      expect(window.confirm).toHaveBeenCalledTimes(1);
      expect(screen.getByRole("tab", { name: "Catalog" })).toHaveAttribute("aria-selected", "true");
      await selectTab("Drafts");
      expect(screen.getByLabelText("Definition label")).toHaveValue("Keep unfinished fields");
      expect(screen.getByLabelText("Audit reason")).toHaveValue("Keep queued corrections");
      vi.mocked(window.confirm).mockReturnValue(true);
      await selectTarget(`${kind}-${sameTarget ? "a" : "b"}`);
      expect(window.confirm).toHaveBeenCalledTimes(2);
      expect(screen.getByRole("tab", { name: "Drafts" })).toHaveAttribute("aria-selected", "true");
      if (kind === "definition") expect(screen.getByLabelText("Definition label")).toHaveValue(sameTarget ? "First definition" : "Second definition");
      else expect(screen.getByLabelText("Correction")).toHaveValue("keep_separate");
      expect(screen.getByLabelText("Audit reason")).toHaveValue("Keep queued corrections");
      expect(screen.getByRole("button", { name: "Remove correction 1" })).toBeVisible();
      await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
      await screen.findByText("Draft saved. Preview its impact before publishing.");
      const write = fetcher.mock.calls.find(([, options]) => options?.method === "PUT")!;
      expect(JSON.parse(write[1]!.body as string)).toMatchObject({ expected_version: 1, expected_revision: 1, reason: "Keep queued corrections", edits: [edit] });
      vi.mocked(window.confirm).mockClear();
      await userEvent.click(screen.getByRole("button", { name: "New draft" }));
      await screen.findByRole("heading", { name: "New correction draft" });
      expect(window.confirm).not.toHaveBeenCalled();
    },
  );

  it("preserves unfinished editor fields while the first save persists only queued corrections", async () => {
    const response = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
    const fetcher = install([], (url, body) => response({ ...draft, version: url.endsWith("/draft-a") ? 2 : 1, reason: body.reason, edits: body.edits }));
    render(<OntologyPanel api={new UserApi("key")} session={session} />);
    await selectTab("Drafts");
    for (const [label, value] of [["Definition key", "storage"], ["Definition label", "Storage"], ["Definition description", "Durable storage"], ["Audit reason", "Preserve unfinished fields"]]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    await userEvent.click(screen.getByRole("button", { name: "Add correction to draft" }));
    for (const [label, value] of [["Definition key", "search"], ["Definition label", "Search"], ["Definition description", "Search organization"]]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft saved. Preview its impact before publishing.");
    expect(screen.getByLabelText("Definition key")).toHaveValue("search");
    expect(screen.getByLabelText("Definition label")).toHaveValue("Search");
    expect(screen.getByLabelText("Definition description")).toHaveValue("Search organization");
    const first = fetcher.mock.calls.find(([, options]) => options?.method === "POST")!;
    expect(JSON.parse(first[1]!.body as string)).toMatchObject({ expected_version: 0, expected_revision: 1, edits: [edit] });
    await userEvent.click(screen.getByRole("button", { name: "Add correction to draft" }));
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft saved. Preview its impact before publishing.");
    const second = fetcher.mock.calls.find(([, options]) => options?.method === "PUT")!;
    expect(JSON.parse(second[1]!.body as string)).toMatchObject({ expected_version: 1, expected_revision: 1, edits: [edit, { action: "define", expected_version: 0, definition: { key: "search", label: "Search", description: "Search organization" } }] });
    expect(window.confirm).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "New draft" }));
    await screen.findByRole("heading", { name: "New correction draft" });
    expect(screen.getByLabelText("Definition key")).toHaveValue("");
    expect(screen.getByLabelText("Audit reason")).toHaveValue("");
  });

  it("keeps corrections on a conflict and requires explicit refresh", async () => {
    install([draft], () => new Response(JSON.stringify({ message: "refresh the draft before resubmitting" }), { status: 409 }));
    render(<OntologyPanel api={new UserApi("key")} session={session} />);
    await selectTab("Drafts");
    fireEvent.click(await screen.findByRole("button", { name: /Storage correction/ }));
    await screen.findByRole("heading", { name: /Draft draft-a/ });
    fireEvent.change(screen.getByLabelText("Audit reason"), { target: { value: "Retained manager edits" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByRole("alert");
    expect(screen.getByLabelText("Audit reason")).toHaveValue("Retained manager edits");
    expect(screen.getByRole("button", { name: "Save draft" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Refresh draft before resubmitting" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save draft" })).toBeEnabled());
    expect(screen.getByLabelText("Audit reason")).toHaveValue("Retained manager edits");
  });

  it.each([true, false])("reuses the operation key and cookie CSRF (UUID available: %s)", async (uuidAvailable) => {
    if (!uuidAvailable) vi.stubGlobal("crypto", { randomUUID: undefined });
    document.cookie = "dense_mem_ui_csrf=ontology-csrf";
    let count = 0;
    const fetcher = install([draft], () => ++count === 1 ? new Response(JSON.stringify({ message: "temporary failure" }), { status: 503 }) : new Response(JSON.stringify({ data: { ...draft, version: 2 } }), { status: 200 }));
    render(<OntologyPanel api={new UserApi("", "api_key_session")} session={session} />);
    await selectTab("Drafts");
    fireEvent.click(await screen.findByRole("button", { name: /Storage correction/ }));
    await screen.findByRole("heading", { name: /Draft draft-a/ });
    fireEvent.change(screen.getByLabelText("Audit reason"), { target: { value: "Unchanged retry" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft saved. Preview its impact before publishing.");
    const writes = fetcher.mock.calls.filter(([, options]) => options?.method === "PUT");
    expect(writes).toHaveLength(2);
    expect(JSON.parse(writes[0][1]!.body as string).operation_key).toBe(JSON.parse(writes[1][1]!.body as string).operation_key);
    expect(writes[0][1]?.headers).toMatchObject({ "X-Dense-Mem-CSRF": "ontology-csrf" });
  });

  it("allows a read-only manager to inspect and preview a saved draft", async () => {
    const fetcher = install([draft], () => new Response(JSON.stringify({ data: { changed_records: 2, affected_records: 0, affected_sources: 0, examples: [], complete: true, requires_reassessment: true } }), { status: 200 }));
    render(<OntologyPanel api={new UserApi("key")} session={{ ...session, membership: { ...session.membership, grants: ["read"] } }} />);
    await selectTab("Drafts");
    fireEvent.click(await screen.findByRole("button", { name: /Storage correction/ }));
    await screen.findByRole("heading", { name: /Draft draft-a/ });
    fireEvent.click(screen.getByRole("button", { name: "Preview impact" }));
    await screen.findByRole("heading", { name: "Impact preview" });
    expect(screen.getByRole("button", { name: "Publish correction" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save draft" })).toBeDisabled();
    expect(fetcher.mock.calls.some(([url]) => url.endsWith("/preview"))).toBe(true);
  });

  it("builds a definition correction using the editor", async () => {
    const fetcher = install();
    render(<OntologyPanel api={new UserApi("key")} session={session} />);
    await selectTab("Drafts");
    await screen.findByRole("heading", { name: "New correction draft" });
    for (const [label, value] of [["Definition key", "storage"], ["Definition label", "Storage"], ["Definition description", "Durable storage"], ["Audit reason", "Manager correction"]]) fireEvent.change(screen.getByLabelText(label), { target: { value } });
    fireEvent.click(screen.getByRole("button", { name: "Add correction to draft" }));
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft saved. Preview its impact before publishing.");
    const call = fetcher.mock.calls.find(([, options]) => options?.method === "POST");
    expect(JSON.parse(call![1]!.body as string)).toMatchObject({ expected_version: 0, expected_revision: 1, edits: [edit] });
  });

  it("keeps creation keys for an ambiguous retry and renews them for a new draft intent", async () => {
    let attempts = 0;
    const fetcher = install([], () => ++attempts === 1
      ? new Response(JSON.stringify({ message: "temporary failure" }), { status: 503 })
      : new Response(JSON.stringify({ data: draft }), { status: 200 }));
    render(<OntologyPanel api={new UserApi("key")} session={session} />);
    await selectTab("Drafts");
    await screen.findByRole("heading", { name: "New correction draft" });
    const fillDraft = () => {
      for (const [label, value] of [["Definition key", "storage"], ["Definition label", "Storage"], ["Definition description", "Durable storage"], ["Audit reason", "Identical new intent"]]) {
        fireEvent.change(screen.getByLabelText(label), { target: { value } });
      }
      fireEvent.click(screen.getByRole("button", { name: "Add correction to draft" }));
    };
    fillDraft();
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft saved. Preview its impact before publishing.");
    await userEvent.click(screen.getByRole("button", { name: "New draft" }));
    await screen.findByRole("heading", { name: "New correction draft" });
    fillDraft();
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft saved. Preview its impact before publishing.");
    const writes = fetcher.mock.calls.filter(([, options]) => options?.method === "POST");
    expect(writes).toHaveLength(3);
    const [initial, retry, next] = writes.map(([, options]) => JSON.parse(options!.body as string));
    expect(retry).toEqual(initial);
    expect({ ...next, operation_key: initial.operation_key }).toEqual(initial);
    expect(next.operation_key).not.toBe(initial.operation_key);
  });

  it("identifies distinct classification targets and correction members in catalog and history", async () => {
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
		if(url.includes("/records"))return response({records,revision:1});
		if(url.includes("/status"))return response(coverage);
		if(url.endsWith("/publications/history-a"))return response({id:"history-a",revision:1,record_views:records});
		if(url.includes("/publications"))return response({publications:[{id:"history-a",revision:1,origin:"manager",reason:"Inspection history"}]});
		if(url.includes("/sources"))return response({sources:[]});return response({drafts:[]});
	}));
	render(<OntologyPanel api={new UserApi("key")} session={session} />);
	await screen.findByText("Classification: First classification (definition-a)");
	expect(screen.getByText("Target: Second classification (definition-b)")).toBeVisible();
	expect(screen.getAllByText(/source-a/)).toHaveLength(2);expect(screen.getByText(/source-b/)).toBeVisible();
	await selectTab("History");
	fireEvent.click(screen.getByRole("button",{name:/Inspection history/}));
	await waitFor(()=>expect(screen.getAllByText("Classification: First classification (definition-a)")).toHaveLength(1));
  });

  it("inspects and pages shared sources, publishes a group correction, and reverses its history", async () => {
    const response = (data: unknown) => new Response(JSON.stringify({ data }), { status: 200 });
    const handles = [{kind:"evidence",id:"source-a",version:1},{kind:"evidence",id:"source-b",version:1}];
    const group = {id:"group-a",kind:"evidence_group",version:1,current:true,group:{members:handles}};
    let revision=1;let saved:OntologyDraft|null=null;let published=false;
    const fetcher=vi.fn(async(url:string,options?:RequestInit)=>{
      const path=new URL(url,"http://localhost");const body=options?.body?JSON.parse(options.body as string):{};
      if(options?.method==="POST"){
        if(path.pathname.endsWith("/preview"))return response({changed_records:1,affected_records:2,affected_sources:12,examples:[{...handles[0],text:"Atlas source",truncated:true}],examples_truncated:true,complete:true,requires_reassessment:true});
        if(path.pathname.endsWith("/rollback")){revision++;return response({id:"rollback-a",revision});}
        if(path.pathname.endsWith("/publish")){published=true;revision++;saved={...saved!,state:"published",version:2,publication:{id:"publication-a",revision,existing:false,records:[{id:"override-a",version:1}]}};return response(saved);}
        saved={...draft,id:"saved-group",reason:body.reason,edits:body.edits,base_revision:revision};return response(saved);
      }
      if(path.pathname.endsWith("/status"))return response({...coverage,revision,counts:{eligible:3,organized:2,pending:1,ambiguous:0,failed:0,budget_deferred:0}});
      if(path.pathname.endsWith("/sources"))return response({sources:path.searchParams.get("cursor")?[{...handles[1],id:"source-c",text:"Additional shared source",truncated:false}]:handles.map((source)=>({...source,text:source.id,truncated:false})),next_cursor:path.searchParams.get("cursor")?undefined:"source-b"});
      if(path.pathname.endsWith("/records"))return response({records:[group],revision,next_cursor:path.searchParams.get("cursor")?undefined:"group-a"});
      if(path.pathname.endsWith("/drafts"))return response({drafts:saved?[saved]:[],next_cursor:path.searchParams.get("cursor")?undefined:"draft-cursor"});
      if(path.pathname.endsWith("/publications"))return response({publications:published?[{id:"publication-a",revision:2,origin:"manager",reason:"Separate source grouping"}]:[],next_revision:path.searchParams.get("after")==="0"?1:0});
      return response({id:"publication-a",revision:2,origin:"manager",reason:"Separate source grouping",record_views:[{id:"override-a",kind:"override",version:1,current:true,override:{action:"keep_separate",members:handles}}]});
    });vi.stubGlobal("fetch",fetcher);
    render(<OntologyPanel api={new UserApi("key")} session={session}/>);
    fireEvent.click(await screen.findByRole("button",{name:"Correct grouping"}));
    expect(screen.getByLabelText("Correction",{exact:true})).toHaveValue("keep_separate");
    await selectTab("Sources");
    fireEvent.click(screen.getByRole("button",{name:"Clear selected sources"}));
    fireEvent.change(screen.getByLabelText("Source type"),{target:{value:"evidence"}});fireEvent.change(screen.getByLabelText("Source text or ID"),{target:{value:"Atlas"}});
    fireEvent.click(screen.getByRole("button",{name:"Inspect sources"}));await waitFor(()=>expect(screen.getByRole("button",{name:"Inspect sources"})).toBeEnabled());
    fireEvent.click(screen.getByRole("button",{name:"Load more sources"}));await screen.findByText("Additional shared source");
    fireEvent.click(screen.getByLabelText("Select source source-a"));fireEvent.click(screen.getByLabelText("Select source source-b"));
    await selectTab("Drafts");
    fireEvent.click(screen.getByRole("button",{name:"Add correction to draft"}));fireEvent.change(screen.getByLabelText("Audit reason"),{target:{value:"Separate source grouping"}});
    fireEvent.click(screen.getByRole("button",{name:"Save draft"}));await screen.findByText("Draft saved. Preview its impact before publishing.");
    fireEvent.click(screen.getByRole("button",{name:"Preview impact"}));await screen.findByText("Examples are truncated; counts cover the stored dependency set.");
    fireEvent.click(screen.getByRole("button",{name:"Publish correction"}));await screen.findByText("Correction published. Affected organization is waiting for reassessment.");
    await selectTab("History");
    fireEvent.click(screen.getByRole("button",{name:/Revision 2 · manager/}));await screen.findByRole("heading",{name:"Publication revision 2"});
    expect(within(screen.getByLabelText("Publication detail")).getByText(/source-a/)).toBeVisible();
    fireEvent.change(screen.getByLabelText("Rollback reason"),{target:{value:"Reverse selected correction"}});fireEvent.click(screen.getByRole("button",{name:"Publish rollback"}));await screen.findByText(/Rollback published as a new revision/);
    const rollbackCall=fetcher.mock.calls.find(([url])=>url.endsWith("/rollback"));expect(JSON.parse(rollbackCall![1]!.body as string)).toMatchObject({expected_revision:2,reason:"Reverse selected correction"});
    fireEvent.click(screen.getByRole("button",{name:"Load more publications"}));await waitFor(()=>expect(fetcher.mock.calls.some(([url])=>url.includes("after=1"))).toBe(true));
    await selectTab("Drafts");
    fireEvent.click(screen.getByRole("button",{name:"Load more drafts"}));await waitFor(()=>expect(fetcher.mock.calls.some(([url])=>url.includes("cursor=draft-cursor"))).toBe(true));
    await selectTab("Catalog");
    fireEvent.click(screen.getByRole("button",{name:"Load more catalog records"}));await waitFor(()=>expect(fetcher.mock.calls.some(([url])=>url.includes("cursor=group-a"))).toBe(true));
    fireEvent.change(screen.getByLabelText("Catalog type"),{target:{value:"evidence_group"}});await waitFor(()=>expect(fetcher.mock.calls.some(([url])=>url.includes("kind=evidence_group"))).toBe(true));
  });

  it("edits definition fields, removes draft edits, unpins explicitly, and discards the saved draft",async()=>{
    const response=(data:unknown)=>new Response(JSON.stringify({data}),{status:200});
    const records=[{id:"class-a",kind:"entity_class",version:3,current:true,definition:{key:"people",label:"People",description:"People class",aliases:["humans"],parent_id:"parent-a",base_entity_kind:"person"}},{id:"parent-a",kind:"entity_class",version:1,current:true,definition:{key:"parent",label:"Parent class",description:"Parent",base_entity_kind:"person"}},{id:"pin-a",kind:"override",version:4,current:true,override:{action:"pin_definition",target_id:"class-a"}}];
    let saved:OntologyDraft|null=null;
    const fetcher=vi.fn(async(url:string,options?:RequestInit)=>{
      const path=new URL(url,"http://localhost").pathname;const body=options?.body?JSON.parse(options.body as string):{};
      if(options?.method==="POST"){if(path.endsWith("/discard")){saved={...saved!,state:"discarded",version:2};return response(saved);}saved={...draft,reason:body.reason,edits:body.edits};return response(saved);}
      if(path.endsWith("/records"))return response({records,revision:1});if(path.endsWith("/status"))return response(coverage);if(path.endsWith("/sources"))return response({sources:[]});if(path.endsWith("/publications"))return response({publications:[]});return response({drafts:saved?[saved]:[]});
    });vi.stubGlobal("fetch",fetcher);render(<OntologyPanel api={new UserApi("key")} session={session}/>);
    fireEvent.click(await screen.findByRole("button",{name:"Edit People"}));await waitFor(()=>expect(screen.getByLabelText("Definition label")).toHaveValue("People"));
    for(const [label,value] of [["Definition key","people-revised"],["Definition label","People revised"],["Definition description","Corrected people class"],["Definition aliases","humans, people"],["Definition parent","parent-a"]])fireEvent.change(screen.getByLabelText(label),{target:{value}});
    fireEvent.change(screen.getByLabelText("Class entity kind"),{target:{value:"person"}});fireEvent.click(screen.getByRole("button",{name:"Add correction to draft"}));
    fireEvent.click(screen.getByRole("button",{name:"Remove correction 1"}));expect(screen.queryByRole("button",{name:"Remove correction 1"})).toBeNull();
    fireEvent.click(screen.getByRole("button",{name:"Add correction to draft"}));await selectTab("Catalog");fireEvent.click(screen.getByRole("button",{name:"Unpin definition"}));fireEvent.change(screen.getByLabelText("Audit reason"),{target:{value:"Edit and unpin definition"}});
    fireEvent.click(screen.getByRole("button",{name:"Save draft"}));await screen.findByText("Draft saved. Preview its impact before publishing.");
    const body=JSON.parse(fetcher.mock.calls.find(([,options])=>options?.method==="POST")![1]!.body as string);
    expect(body.edits).toEqual([{action:"define",record_id:"class-a",expected_version:3,kind:"entity_class",definition:{key:"people-revised",label:"People revised",description:"Corrected people class",aliases:["humans","people"],parent_id:"parent-a",base_entity_kind:"person"}},{action:"remove_override",record_id:"pin-a",expected_version:4}]);
    fireEvent.click(screen.getByRole("button",{name:"Discard draft"}));await screen.findByText("Draft discarded. Its history is retained.");expect(screen.queryByRole("button",{name:"Preview impact"})).toBeNull();
    await userEvent.click(screen.getByRole("button",{name:"New draft"}));fireEvent.change(screen.getByLabelText("Definition type"),{target:{value:"topic"}});expect(screen.getByLabelText("Definition parent")).toHaveValue("");
  });

  it("refreshes current record and source versions while preserving the manager's unsaved reason",async()=>{
    const response=(data:unknown)=>new Response(JSON.stringify({data}),{status:200});
    const saved={...draft,base_revision:0,edits:[{action:"classify",record_id:"override-a",expected_version:1,source:{kind:"relationship",id:"relationship-a",version:1},definition_id:"topic-a"}]};
    let refreshedBody:Record<string,unknown>|null=null;
    vi.stubGlobal("fetch",vi.fn(async(url:string,options?:RequestInit)=>{
      const path=new URL(url,"http://localhost").pathname;
      if(options?.method==="PUT"){refreshedBody=JSON.parse(options.body as string);return response({...saved,version:5,base_revision:1,reason:refreshedBody!.reason,edits:refreshedBody!.edits});}
      if(path.endsWith("/records/override-a"))return response({version:3});if(path.endsWith("/records"))return response({records:[],revision:1});if(path.endsWith("/status"))return response(coverage);if(path.endsWith("/sources"))return response({sources:[{kind:"relationship",id:"relationship-a",version:2,text:"Updated source",truncated:false}]});if(path.endsWith("/publications"))return response({publications:[]});if(path.endsWith("/drafts"))return response({drafts:[saved]});return response({...saved,version:4});
    }));render(<OntologyPanel api={new UserApi("key")} session={session}/>);
    await selectTab("Drafts");
    fireEvent.click(await screen.findByRole("button",{name:/Storage correction/}));await screen.findByText("This draft uses an older catalog revision. Refresh it before publishing.");
    fireEvent.change(screen.getByLabelText("Audit reason"),{target:{value:"Keep my unsaved reason"}});fireEvent.click(screen.getByRole("button",{name:"Refresh draft before resubmitting"}));await screen.findByText("Draft refreshed. Review the corrections, save, and preview again.");
    fireEvent.click(screen.getByRole("button",{name:"Save draft"}));await screen.findByText("Draft saved. Preview its impact before publishing.");
    expect(refreshedBody).toMatchObject({expected_version:4,expected_revision:1,reason:"Keep my unsaved reason",edits:[{expected_version:3,source:{kind:"relationship",id:"relationship-a",version:2}}]});
  });
});

async function selectTab(name: string) { await userEvent.click(await screen.findByRole("tab", { name })); }
