import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/primitives/tabs";
import { useConfirmation } from "../ui/confirmation";
import { useUnsavedChanges } from "../ui/navigation";
import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Table, TableHeader, TableRow, TableHead, TableBody, TableCell } from "../ui/primitives/table";
import { Input } from "../ui/primitives/input";
import { Textarea } from "../ui/primitives/textarea";
import { useEffect, useRef, useState } from "react";
import { FieldRow, LoadingState, SectionHeading, UsageMeter } from "../ui/components";
import { ApiError, newIdempotencyKey, UserApi, UserSession } from "./api";
import { TopicEditor } from "./TopicEditor";
import { type TopicDefinition, type TopicCoverage, type TopicDraft, type TopicEdit, type TopicKind, type TopicPreview, type TopicPublication, type TopicRecord, type TopicSource, type SourceHandle, type SourceKind } from "./topic-types";

const sourceKey = (source: SourceHandle) => `${source.kind}:${source.id}`;
const recordLabel = (record: TopicRecord) => record.definition?.label ?? (record.group ? `${record.group.members.length} ${"evidence"} sources` : record.override?.action.replaceAll("_", " ") ?? record.kind.replaceAll("_", " "));

export function TopicsPanel({ api, session }: { api: UserApi; session: UserSession }) {
  const confirm = useConfirmation();
  const [activeTab, setActiveTab] = useState("catalog");
  const [editorDirty, setEditorDirty] = useState(false);
  const [editorEpoch, setEditorEpoch] = useState(0);
  const [records, setRecords] = useState<TopicRecord[]>([]);
  const [recordKind, setRecordKind] = useState<TopicKind | "">("");
  const [recordCursor, setRecordCursor] = useState("");
  const [sources, setSources] = useState<TopicSource[]>([]);
  const [sourceKind, setSourceKind] = useState<SourceKind>("evidence");
  const [sourceQuery, setSourceQuery] = useState("");
  const [sourceCursor, setSourceCursor] = useState("");
  const [target, setTarget] = useState<TopicRecord | null>(null);
  const [coverage, setCoverage] = useState<TopicCoverage | null>(null);
  const [drafts, setDrafts] = useState<TopicDraft[]>([]);
  const [draftCursor, setDraftCursor] = useState("");
  const [draft, setDraft] = useState<TopicDraft | null>(null);
  const [edits, setEdits] = useState<TopicEdit[]>([]);
  const [reason, setReason] = useState("");
  const [dirty, setDirty] = useState(false);
  const [preview, setPreview] = useState<TopicPreview | null>(null);
  const [publications, setPublications] = useState<TopicPublication[]>([]);
  const [historyCursor, setHistoryCursor] = useState(0);
  const [publication, setPublication] = useState<TopicPublication | null>(null);
  const [rollbackReason, setRollbackReason] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [fresh, setFresh] = useState(false);
  useUnsavedChanges(dirty || editorDirty || Boolean(rollbackReason));
  const operationKeys = useRef(new Map<string, string>());
  const canWrite = session.membership.grants.includes("write");
  const open = !draft || draft.state === "open";
  const stale = Boolean(draft && coverage && draft.base_revision !== coverage.revision);

  function operationKey(action: string, body: unknown) {
    const signature = JSON.stringify([action, draft?.id ?? "", body]);
    let value = operationKeys.current.get(signature);
    if (!value) { value = newIdempotencyKey("topics"); operationKeys.current.set(signature, value); }
    return value;
  }

  async function load() {
    setPreview(null); setFresh(false);
    const [catalog, status, saved, history] = await Promise.all([api.topicRecords(recordKind), api.topicStatus(), api.topicDrafts(), api.topicPublications()]);
    setRecords(catalog.records); setRecordCursor(catalog.next_cursor ?? ""); setCoverage(status);
    setDrafts(saved.drafts); setDraftCursor(saved.next_cursor ?? ""); setPublications(history.publications); setHistoryCursor(history.next_revision ?? 0); setFresh(true);
  }

  useEffect(() => {
    let active = true;
    Promise.all([api.topicRecords(), api.topicStatus(), api.topicDrafts(), api.topicPublications(), api.topicSources()])
      .then(([catalog, status, saved, history, sourcePage]) => {
        if (!active) return;
        setRecords(catalog.records); setRecordCursor(catalog.next_cursor ?? ""); setCoverage(status);
        setDrafts(saved.drafts); setDraftCursor(saved.next_cursor ?? ""); setPublications(history.publications); setHistoryCursor(history.next_revision ?? 0);
        setSources(sourcePage.sources); setSourceCursor(sourcePage.next_cursor ?? ""); setFresh(true);
      }).catch((err) => { if (active) setError(readError(err)); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [api, session.team.id]);

  async function perform(work: () => Promise<void>) {
    setBusy(true); setError(""); setMessage("");
    try { await work(); } catch (err) { if (err instanceof ApiError && err.status === 409) { setFresh(false); setPreview(null); } setError(readError(err)); } finally { setBusy(false); }
  }

  function add(edit: TopicEdit) { setActiveTab("drafts"); setEdits((current) => [...current, edit]); setDirty(true); setPreview(null); setMessage("Correction added. Save the draft to validate it."); }
  async function canReplaceDraft() {
    return !(dirty || editorDirty) || await confirm({ title: "Discard unsaved draft edits?", description: "Saved drafts remain available. Unsaved corrections and editor fields will be lost.", confirmLabel: "Discard edits", destructive: true });
  }

  async function selectTarget(record: TopicRecord) {
    if (editorDirty && !await confirm({ title: "Discard editor changes?", description: "Unsaved editor fields will be lost. Queued corrections and the audit reason will be retained.", confirmLabel: "Discard edits", destructive: true })) return;
    setEditorDirty(false); setEditorEpoch((value) => value + 1); setTarget(record); setActiveTab("drafts");
    if (record.definition) setMessage("Definition selected. Edit its fields in this draft.");
  }

  async function startDraft() { if (!await canReplaceDraft()) return; setEditorEpoch((value) => value + 1); setEditorDirty(false); operationKeys.current.clear(); setDraft(null); setEdits([]); setReason(""); setDirty(false); setPreview(null); setTarget(null); setMessage(""); }

  async function openDraft(id: string) {
    if (!await canReplaceDraft()) return;
    const next = await api.topicDraft(id); setEditorEpoch((value) => value + 1); setEditorDirty(false); setDraft(next); setEdits(next.edits); setReason(next.reason); setDirty(false); setPreview(null); setTarget(null);
  }

  async function save() {
    const body = { expected_version: draft?.version ?? 0, expected_revision: draft?.base_revision ?? coverage!.revision, reason, edits };
    const next = await api.saveTopicDraft(draft?.id ?? "", { ...body, operation_key: operationKey("save", body) });
    setDraft(next); setDirty(false); setPreview(null); setDrafts((current) => [next, ...current.filter((item) => item.id !== next.id)]); setMessage("Draft saved. Preview its impact before publishing.");
  }

  async function refreshDraft() {
    const status = await api.topicStatus();
    const nextEdits = await Promise.all(edits.map(async (edit) => {
      const next = { ...edit };
      if (edit.record_id) next.expected_version = (await api.topicRecord(edit.record_id)).version;
      const refreshSource = async (source: SourceHandle) => {
        const found = (await api.topicSources(source.kind, "", source.id)).sources.find((item) => item.id === source.id);
        if (!found) throw new Error("A selected source is unavailable. Remove or replace that correction.");
        return { kind: found.kind, id: found.id, version: found.version };
      };
      if (edit.source) next.source = await refreshSource(edit.source);
      if (edit.members) next.members = await Promise.all(edit.members.map(refreshSource));
      return next;
    }));
    if (draft) { const saved = await api.topicDraft(draft.id); setDraft({ ...saved, base_revision: status.revision }); }
    setEdits(nextEdits); setCoverage(status); setDirty(true); setPreview(null); setFresh(true); setMessage("Draft refreshed. Review the corrections, save, and preview again.");
  }

  async function command(action: "publish" | "discard") {
    if (!await confirm({ title: action === "publish" ? "Publish topic changes?" : "Discard saved draft?", description: action === "publish" ? "This publishes the reviewed impact and queues affected organization for reassessment." : "This closes the draft and retains its history.", confirmLabel: action === "publish" ? "Publish topic changes" : "Discard draft", destructive: action === "discard" })) return;
    const body = { expected_version: draft!.version, expected_revision: draft!.base_revision, reason };
    const next = await api.commandTopicDraft(draft!.id, action, { ...body, operation_key: operationKey(action, body) });
    setDraft(next); setDirty(false); setPreview(null);
    setMessage(action === "publish" ? "Correction published. Affected organization is waiting for reassessment." : "Draft discarded. Its history is retained.");
    await load();
  }

  async function catalog(kind: TopicKind | "", more = false) {
    const page = await api.topicRecords(kind, more ? recordCursor : "");
    setRecords((current) => more ? [...current, ...page.records] : page.records); setRecordCursor(page.next_cursor ?? ""); setRecordKind(kind); setPreview(null);
  }

  async function sourcePage(more = false) {
    const page = await api.topicSources(sourceKind, more ? sourceCursor : "", sourceQuery);
    setSources((current) => more ? [...current, ...page.sources] : page.sources); setSourceCursor(page.next_cursor ?? "");
  }

  async function rollback() {
    if (!await confirm({ title: "Publish rollback?", description: "This creates a new topic revision. The server will verify that the records and sources are still eligible.", confirmLabel: "Publish rollback", destructive: true })) return;
    const body = { expected_revision: coverage!.revision, reason: rollbackReason };
    const signature = JSON.stringify(["rollback", publication!.id, body]);
    let key = operationKeys.current.get(signature); if (!key) { key = newIdempotencyKey("topics"); operationKeys.current.set(signature, key); }
    await api.rollbackTopicPublication(publication!.id, { ...body, operation_key: key });
    setPublication(null); setRollbackReason(""); setMessage("Rollback published as a new revision. Affected organization is waiting for reassessment."); await load();
  }

  async function selectPublication(id: string) {
    if (rollbackReason && !await confirm({ title: "Discard rollback reason?", description: "Opening this publication will clear your unsaved rollback reason.", confirmLabel: "Discard reason", destructive: true })) return;
    setPublication(await api.topicPublication(id));
    setRollbackReason("");
  }

  if (loading) return <LoadingState label="Loading topic" />;
  return <div className="stack">
    <SectionHeading title="Topics" subtitle="Browse shared Topics and publish catalog edits." actions={<Button variant="outline" className="ghost-button" disabled={busy} onClick={() => void perform(load)}>Refresh topics</Button>} />
    {error && <p className="field-error" role="alert">{error}</p>}{message && <p role="status">{message}</p>}
    {coverage && <section className="topic-team-coverage" aria-label="Team topic coverage">
      <SectionHeading title="Team processing coverage" actions={<span className={`status-pill ${coverage.coverage_complete ? "success" : "warning"}`}>{coverage.coverage_complete ? "Current" : "Reassessment pending"}</span>} />
      <UsageMeter label="Assigned / eligible" value={coverage.counts.organized} limit={coverage.counts.eligible} detail={`Revision ${coverage.revision} · ${coverage.counts.eligible ? "discovered shared records" : "no eligible shared records discovered"}`} />
      <dl className="summary-grid"><div><dt>Pending</dt><dd>{coverage.counts.pending.toLocaleString()}</dd></div><div><dt>Failed</dt><dd>{coverage.counts.failed.toLocaleString()}</dd></div><div><dt>Uncategorized</dt><dd>{(coverage.counts.uncategorized ?? 0).toLocaleString()}</dd></div><div><dt>Ambiguous</dt><dd>{coverage.counts.ambiguous.toLocaleString()}</dd></div></dl>
      {!coverage.discovery_complete && <p className="banner warning">Discovery is in progress. Counts cover discovered shared records.</p>}
      <p className="form-meta">Processing runs under the operator’s maintenance schedule. Refresh to check progress.</p>
    </section>}
    <Tabs className="panel-tabs topic-tabs" value={activeTab} onValueChange={setActiveTab}>
      <TabsList aria-label="Topic sections"><TabsTrigger value="catalog">Catalog</TabsTrigger><TabsTrigger value="sources">Sources</TabsTrigger><TabsTrigger value="drafts">Drafts</TabsTrigger><TabsTrigger value="history">History</TabsTrigger></TabsList>
    <TabsContent value="catalog"><section className="surface" aria-label="Topic catalog"><h3>Catalog and groups</h3>
      <Label>Catalog type<NativeSelect aria-label="Catalog type" value={recordKind} disabled={busy} onChange={(event) => void perform(() => catalog(event.target.value as TopicKind | ""))}><NativeSelectOption value="">All records</NativeSelectOption>{["topic", "assignment", "evidence_group"].map((kind) => <NativeSelectOption key={kind} value={kind}>{kind.replaceAll("_", " ")}</NativeSelectOption>)}</NativeSelect></Label>
      <div className="table-wrap"><Table className="data-table"><TableHeader><TableRow><TableHead>Organization</TableHead><TableHead>Status</TableHead><TableHead>Sources or classification</TableHead><TableHead>Actions</TableHead></TableRow></TableHeader><TableBody>{records.map((record) => <TableRow key={record.id}>
        <TableCell><strong>{recordLabel(record)}</strong>{record.definition && <p>{record.definition.description}</p>}<small title={record.id}>{record.id.slice(0, 8)} · revision {record.version}</small></TableCell>
        <TableCell><span className={`status-pill ${record.retired ? "neutral" : record.current ? "success" : "warning"}`}>{record.retired ? "Retired" : record.current ? "Current" : "Needs reassessment"}</span></TableCell>
        <TableCell><RecordDetails record={record} records={records} /></TableCell>
        <TableCell>{!record.retired && record.definition && <Button variant="outline" disabled={busy || !canWrite} onClick={() => void selectTarget(record)}>Edit {record.definition.label}</Button>}
        </TableCell></TableRow>)}</TableBody></Table></div>
      {!records.length && <p>No catalog records yet.</p>}{recordCursor && <Button variant="outline" disabled={busy} onClick={() => void perform(() => catalog(recordKind, true))}>Load more catalog records</Button>}
    </section></TabsContent>
    <TabsContent value="sources"><section className="surface"><h3>Shared sources</h3>
      <Label>Source type<NativeSelect aria-label="Source type" value={sourceKind} disabled={busy} onChange={(event) => { setSourceKind(event.target.value as SourceKind); setSources([]); setSourceCursor(""); }}>{["evidence"].map((kind) => <NativeSelectOption key={kind}>{kind}</NativeSelectOption>)}</NativeSelect></Label>
      <Label>Source text or ID<Input aria-label="Source text or ID" value={sourceQuery} maxLength={128} onChange={(event) => setSourceQuery(event.target.value)} /></Label><Button variant="outline" disabled={busy} onClick={() => void perform(() => sourcePage())}>Inspect sources</Button>
      {sources.map((source) => <p key={sourceKey(source)}>{source.text}{source.truncated && " … (excerpt)"}</p>)}
      {sourceCursor && <Button variant="outline" disabled={busy} onClick={() => void perform(() => sourcePage(true))}>Load more sources</Button>}<p>Text filtering applies to each inspected page. Load more sources to continue searching. A source ID looks up that shared source directly.</p>
    </section></TabsContent>
    <TabsContent value="drafts" forceMount hidden={activeTab !== "drafts"}><section className="surface"><SectionHeading title="Saved drafts" subtitle="Only your drafts are listed." actions={<Button variant="outline" disabled={busy || !canWrite} onClick={startDraft}>New draft</Button>} />
      {drafts.map((item) => <Button variant="outline" key={item.id} disabled={busy} onClick={() => void perform(() => openDraft(item.id))}>{item.reason} · {item.state} · revision {item.version}</Button>)}
      {draftCursor && <Button variant="outline" disabled={busy} onClick={() => void perform(async () => { const page = await api.topicDrafts(draftCursor); setDrafts((current) => [...current, ...page.drafts]); setDraftCursor(page.next_cursor ?? ""); })}>Load more drafts</Button>}
      <h3>{draft ? `Draft ${draft.id.slice(0, 8)} · ${draft.state}` : "New topic draft"}</h3>
      {stale && <p role="alert">This draft uses an older catalog revision. Refresh it before publishing.</p>}
      <FieldRow label="Audit reason" htmlFor="topic-audit-reason"><Textarea id="topic-audit-reason" value={reason} maxLength={512} disabled={busy || !canWrite || !open} onChange={(event) => { setReason(event.target.value); setDirty(true); setPreview(null); }} /></FieldRow>
      <TopicEditor key={editorEpoch} onDirtyChange={setEditorDirty} records={records} target={target} disabled={busy || !canWrite || !open || edits.length >= 64} onAdd={add} />
      <ol>{edits.map((edit, index) => <li key={index}><EditDetails edit={edit} records={records} />{open && <Button variant="outline" aria-label={`Remove correction ${index + 1}`} disabled={busy || !canWrite} onClick={() => { setEdits((current) => current.filter((_, position) => position !== index)); setDirty(true); setPreview(null); }}>Remove</Button>}</li>)}</ol>
      <Button variant="default" className="primary-button" disabled={busy || !canWrite || !open || !fresh || !reason.trim() || !edits.length || Boolean(draft && !dirty)} onClick={() => void perform(save)}>Save draft</Button>
      {draft?.state === "open" && <>
        <Button variant="outline" disabled={busy || dirty || stale || !fresh} onClick={() => void perform(async () => { setPreview(await api.previewTopicDraft(draft.id, draft.version)); })}>Preview impact</Button>
        <Button variant="outline" disabled={busy || !canWrite} onClick={() => void perform(refreshDraft)}>Refresh draft before resubmitting</Button>
        <Button variant="outline" disabled={busy || !canWrite || dirty || !reason.trim()} onClick={() => void perform(() => command("discard"))}>Discard draft</Button>
      </>}
      {preview && <div aria-label="Impact preview"><h3>Impact preview</h3><p>{preview.changed_records} changes; {preview.affected_records} affected existing records; {preview.affected_sources} currently available sources.</p>
        <ul>{preview.examples.map((source) => <li key={sourceKey(source)}>{source.text}{source.truncated && " … (excerpt)"}</li>)}</ul>{preview.examples_truncated && <p>Examples are truncated; counts cover the stored dependency set.</p>}
        {preview.requires_reassessment && <p>Publication invalidates affected organization immediately. Automatic reassessment may classify additional related sources later.</p>}
        <Button variant="default" className="primary-button" disabled={busy || !canWrite || !preview.complete || dirty || stale || !fresh} onClick={() => void perform(() => command("publish"))}>Publish topic changes</Button>
      </div>}
    </section></TabsContent>
    <TabsContent value="history" forceMount hidden={activeTab !== "history"}><section className="surface"><h3>Publication history</h3>{publications.map((item) => <Button variant="outline" key={item.id} disabled={busy} onClick={() => void perform(() => selectPublication(item.id))}>Revision {item.revision} · {item.origin} · {item.reason}</Button>)}
      {historyCursor > 0 && <Button variant="outline" disabled={busy} onClick={() => void perform(async () => { const page = await api.topicPublications(historyCursor); setPublications((current) => [...current, ...page.publications]); setHistoryCursor(page.next_revision ?? 0); })}>Load more publications</Button>}
      {publication && <div aria-label="Publication detail"><h3>Publication revision {publication.revision}</h3><p>{publication.reason}</p><ul>{publication.record_views?.map((record) => <li key={record.id}>{recordLabel(record)} · revision {record.version} · {record.current ? "Current" : "Historical or awaiting reassessment"}<RecordDetails record={record} records={publication.record_views ?? []} /></li>)}</ul>
        {(publication.origin === "manager" || publication.origin === "rollback") && <><FieldRow label="Rollback reason" htmlFor="topic-rollback-reason"><Textarea id="topic-rollback-reason" value={rollbackReason} maxLength={512} disabled={busy || !canWrite} onChange={(event) => setRollbackReason(event.target.value)} /></FieldRow><Button variant="outline" disabled={busy || !canWrite || !fresh || !rollbackReason.trim()} onClick={() => void perform(rollback)}>Publish rollback</Button><p>Rollback creates a new revision and is rejected if the changed records or their sources are no longer eligible.</p></>}
      </div>}
    </section></TabsContent>
    </Tabs>
  </div>;
}

function readError(error: unknown) { return error instanceof Error ? error.message : "Topic catalog request failed."; }

function definitionLabel(id: string, records: TopicRecord[]) {
  const label = records.find((record) => record.id === id)?.definition?.label;
  return label ? `${label} (${id})` : id;
}

function SourceDetails({ source }: { source: SourceHandle }) {
  return <span>{source.kind} {source.id} · version {source.version}</span>;
}

function DefinitionParent({ definition, records }: { definition: TopicDefinition; records: TopicRecord[] }) {
  return <p>{definition.parent_id ? `Parent: ${definitionLabel(definition.parent_id, records)}` : "No parent"}</p>;
}

function RecordDetails({ record, records }: { record: TopicRecord; records: TopicRecord[] }) {
  return <div>
    {record.definition && <><p>Key: {record.definition.key}</p><p>Aliases: {record.definition.aliases?.join(", ") || "None"}</p><DefinitionParent definition={record.definition} records={records} /></>}
    {record.assignment && <p><SourceDetails source={record.assignment.source} /> → {definitionLabel(record.assignment.definition_id, records)}</p>}
    {record.community_parent_id && <p>Community: {definitionLabel(record.community_parent_id, records)}</p>}
    {record.override?.target_id && <p>Target: {definitionLabel(record.override.target_id, records)}</p>}
    {record.override?.definition_id && <p>Classification: {definitionLabel(record.override.definition_id, records)}</p>}
    {(record.group?.members ?? record.override?.members)?.map((member) => <p key={sourceKey(member)}><SourceDetails source={member} /></p>)}
  </div>;
}

function EditDetails({ edit, records }: { edit: TopicEdit; records: TopicRecord[] }) {
  return <div>{edit.action.replaceAll("_", " ")}
    {edit.definition && <><p>{edit.definition.label} · {edit.definition.description} · aliases: {edit.definition.aliases?.join(", ") || "None"}</p><DefinitionParent definition={edit.definition} records={records} /></>}
    {edit.record_id && <p>Target record: {edit.record_id} · expected version {edit.expected_version}</p>}
    {edit.definition_id && <p>Definition: {definitionLabel(edit.definition_id, records)}</p>}
    {edit.source && <p><SourceDetails source={edit.source} /></p>}
    {edit.members?.map((member) => <p key={sourceKey(member)}><SourceDetails source={member} /></p>)}
  </div>;
}
