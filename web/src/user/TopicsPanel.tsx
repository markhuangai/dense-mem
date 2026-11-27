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
import { type TopicDefinition, type TopicCoverage, type TopicDraft, type TopicEdit, type TopicKind, type TopicPublication, type TopicRecord, type TopicSource, type SourceHandle, type SourceKind } from "./topic-types";

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
  const [edits, setEdits] = useState<TopicEdit[]>([]);
  const [reason, setReason] = useState("");
  const [publications, setPublications] = useState<TopicPublication[]>([]);
  const [historyCursor, setHistoryCursor] = useState(0);
  const [publication, setPublication] = useState<TopicPublication | null>(null);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [stale, setStale] = useState(false);
  useUnsavedChanges(edits.length > 0 || editorDirty || Boolean(reason));
  const operationKeys = useRef(new Map<string, string>());
  const canWrite = session.membership.grants.includes("write");
  async function load() {
    const [catalog, status, saved, history] = await Promise.all([api.topicRecords(recordKind), api.topicStatus(), api.topicDrafts(), api.topicPublications()]);
    setRecords(catalog.records); setRecordCursor(catalog.next_cursor ?? ""); setCoverage(status);
    setDrafts(saved.drafts); setDraftCursor(saved.next_cursor ?? ""); setPublications(history.publications); setHistoryCursor(history.next_revision ?? 0);
  }

  useEffect(() => {
    let active = true;
    Promise.all([api.topicRecords(), api.topicStatus(), api.topicDrafts(), api.topicPublications(), api.topicSources()])
      .then(([catalog, status, saved, history, sourcePage]) => {
        if (!active) return;
        setRecords(catalog.records); setRecordCursor(catalog.next_cursor ?? ""); setCoverage(status);
        setDrafts(saved.drafts); setDraftCursor(saved.next_cursor ?? ""); setPublications(history.publications); setHistoryCursor(history.next_revision ?? 0);
        setSources(sourcePage.sources); setSourceCursor(sourcePage.next_cursor ?? "");
      }).catch((err) => { if (active) setError(readError(err)); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [api, session.team.id]);

  async function perform(work: () => Promise<void>) {
    setBusy(true); setError(""); setMessage("");
    try { await work(); } catch (err) { if (err instanceof ApiError && err.status === 409) setStale(true); setError(readError(err)); } finally { setBusy(false); }
  }

  function add(edit: TopicEdit) {
    setEdits((current) => {
      const index = edit.record_id ? current.findIndex((item) => item.record_id === edit.record_id) : -1;
      return index < 0 ? [...current, edit] : current.map((item, position) => position === index ? edit : item);
    });
  }

  async function selectTarget(record: TopicRecord | null) {
    if (editorDirty && !await confirm({
      title: "Discard editor changes?",
      description: "Changes already added to the Save list will be kept.",
      confirmLabel: "Discard changes",
      destructive: true,
    })) return;
    setEditorDirty(false);
    setEditorEpoch((value) => value + 1);
    setTarget(record);
    setActiveTab("edit");
  }

  async function save() {
    const body = { reason, edits };
    const signature = JSON.stringify(body);
    let key = operationKeys.current.get(signature);
    if (!key) {
      key = newIdempotencyKey("topics");
      operationKeys.current.set(signature, key);
    }
    await api.saveTopicEdits({ ...body, operation_key: key });
    operationKeys.current.clear();
    setReason("");
    setEdits([]);
    setTarget(null);
    setEditorEpoch((value) => value + 1);
    setMessage("Topic changes saved.");
    await load();
  }

  async function refreshEdits() {
    const selected = target;
    const current = selected ? await api.topicRecord(selected.id) : null;
    await load();
    setTarget(current);
    setEdits([]);
    setEditorDirty(false);
    setEditorEpoch((value) => value + 1);
    operationKeys.current.clear();
    setStale(false);
    setMessage("Topic records refreshed. Review the current details and add your changes again.");
  }

  async function catalog(kind: TopicKind | "", more = false) {
    const page = await api.topicRecords(kind, more ? recordCursor : "");
    setRecords((current) => more ? [...current, ...page.records] : page.records); setRecordCursor(page.next_cursor ?? ""); setRecordKind(kind);
  }

  async function sourcePage(more = false) {
    const page = await api.topicSources(sourceKind, more ? sourceCursor : "", sourceQuery);
    setSources((current) => more ? [...current, ...page.sources] : page.sources); setSourceCursor(page.next_cursor ?? "");
  }

  if (loading) return <LoadingState label="Loading topic" />;
  return <div className="stack">
    <SectionHeading title="Topics" subtitle="Browse and edit shared Topics." actions={<Button variant="outline" className="ghost-button" disabled={busy} onClick={() => void perform(load)}>Refresh topics</Button>} />
    {error && <p className="field-error" role="alert">{error}</p>}{message && <p role="status">{message}</p>}
    {coverage && <section className="topic-team-coverage" aria-label="Team topic coverage">
      <SectionHeading title="Team processing coverage" actions={<span className={`status-pill ${coverage.coverage_complete ? "success" : "warning"}`}>{coverage.coverage_complete ? "Current" : "Reassessment pending"}</span>} />
      <UsageMeter label="Assigned / eligible" value={coverage.counts.organized} limit={coverage.counts.eligible} detail={`Revision ${coverage.revision} · ${coverage.counts.eligible ? "discovered shared records" : "no eligible shared records discovered"}`} />
      <dl className="summary-grid"><div><dt>Pending</dt><dd>{coverage.counts.pending.toLocaleString()}</dd></div><div><dt>Failed</dt><dd>{coverage.counts.failed.toLocaleString()}</dd></div><div><dt>Uncategorized</dt><dd>{(coverage.counts.uncategorized ?? 0).toLocaleString()}</dd></div><div><dt>Ambiguous</dt><dd>{coverage.counts.ambiguous.toLocaleString()}</dd></div></dl>
      {!coverage.discovery_complete && <p className="banner warning">Discovery is in progress. Counts cover discovered shared records.</p>}
      <p className="form-meta">Processing runs under the operator’s maintenance schedule. Refresh to check progress.</p>
    </section>}
    <Tabs className="panel-tabs topic-tabs" value={activeTab} onValueChange={setActiveTab}>
      <TabsList aria-label="Topic sections"><TabsTrigger value="catalog">Catalog</TabsTrigger><TabsTrigger value="sources">Sources</TabsTrigger>{canWrite && <TabsTrigger value="edit">Edit</TabsTrigger>}<TabsTrigger value="history">History</TabsTrigger></TabsList>
    <TabsContent value="catalog"><section className="surface" aria-label="Topic catalog"><SectionHeading title="Catalog and groups" actions={canWrite && <Button variant="outline" disabled={busy} onClick={() => void selectTarget(null)}>New topic</Button>} />
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
    {canWrite && <TabsContent value="edit" forceMount hidden={activeTab !== "edit"}><section className="surface">
      <h3>{target?.definition ? `Edit ${target.definition.label}` : "New topic"}</h3>
      {stale && <Button variant="outline" disabled={busy} onClick={() => void perform(refreshEdits)}>Refresh records</Button>}
      <TopicEditor key={editorEpoch} onDirtyChange={setEditorDirty} records={records} target={target} disabled={busy || stale || edits.length >= 64} onAdd={add} />
      <ol>{edits.map((edit, index) => <li key={index}>
        <EditDetails edit={edit} records={records} />
        <Button variant="ghost-destructive" aria-label={`Remove correction ${index + 1}`} disabled={busy} onClick={() => setEdits((current) => current.filter((_, position) => position !== index))}>Remove</Button>
      </li>)}</ol>
      <FieldRow label="Audit reason" htmlFor="topic-audit-reason"><Textarea id="topic-audit-reason" value={reason} maxLength={512} disabled={busy} onChange={(event) => setReason(event.target.value)} /></FieldRow>
      <Button className="primary-button" disabled={busy || stale || editorDirty || !reason.trim() || !edits.length} onClick={() => void perform(save)}>Save</Button>
    </section></TabsContent>}
    <TabsContent value="history" forceMount hidden={activeTab !== "history"}><section className="surface"><h3>Publication history</h3>{publications.map((item) => <button type="button" className="interactive-card" key={item.id} disabled={busy} onClick={() => void perform(async () => setPublication(await api.topicPublication(item.id)))}>Revision {item.revision} · {item.origin} · {item.reason}</button>)}
      {historyCursor > 0 && <Button variant="outline" disabled={busy} onClick={() => void perform(async () => { const page = await api.topicPublications(historyCursor); setPublications((current) => [...current, ...page.publications]); setHistoryCursor(page.next_revision ?? 0); })}>Load more publications</Button>}
      {publication && <div aria-label="Publication detail"><h3>Publication revision {publication.revision}</h3><p>{publication.reason}</p><ul>{publication.record_views?.map((record) => <li key={record.id}>{recordLabel(record)} · revision {record.version} · {record.current ? "Current" : "Historical or awaiting reassessment"}<RecordDetails record={record} records={publication.record_views ?? []} /></li>)}</ul>
      </div>}
      {drafts.length > 0 && <section aria-label="Previous drafts">
        <h3>Previous drafts</h3>
        {drafts.map((item) => <details key={item.id}>
          <summary>{item.reason} · {item.state} · revision {item.version}</summary>
          <ul>{item.edits.map((edit, index) => <li key={index}><EditDetails edit={edit} records={records} /></li>)}</ul>
        </details>)}
        {draftCursor && <Button variant="outline" disabled={busy} onClick={() => void perform(async () => {
          const page = await api.topicDrafts(draftCursor);
          setDrafts((current) => [...current, ...page.drafts]);
          setDraftCursor(page.next_cursor ?? "");
        })}>Load more previous drafts</Button>}
      </section>}
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
