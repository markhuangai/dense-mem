import { useEffect, useRef, useState } from "react";
import { LoadingState, SectionHeading } from "../ui/components";
import { ApiError, newIdempotencyKey, UserApi, UserSession } from "./api";
import { OntologyEditor } from "./OntologyEditor";
import type { OntologyCoverage, OntologyDraft, OntologyEdit, OntologyKind, OntologyPreview, OntologyPublication, OntologyRecord, OntologySource, SourceHandle, SourceKind } from "./ontology-types";

const sourceKey = (source: SourceHandle) => `${source.kind}:${source.id}`;
const recordLabel = (record: OntologyRecord) => record.definition?.label ?? (record.group ? `${record.group.members.length} ${record.kind === "evidence_group" ? "evidence" : "relationship"} sources` : record.override?.action.replaceAll("_", " ") ?? record.kind.replaceAll("_", " "));

export function OntologyPanel({ api, session }: { api: UserApi; session: UserSession }) {
  const [records, setRecords] = useState<OntologyRecord[]>([]);
  const [recordKind, setRecordKind] = useState<OntologyKind | "">("");
  const [recordCursor, setRecordCursor] = useState("");
  const [sources, setSources] = useState<OntologySource[]>([]);
  const [sourceKind, setSourceKind] = useState<SourceKind>("evidence");
  const [sourceQuery, setSourceQuery] = useState("");
  const [sourceCursor, setSourceCursor] = useState("");
  const [selected, setSelected] = useState<SourceHandle[]>([]);
  const [target, setTarget] = useState<OntologyRecord | null>(null);
  const [coverage, setCoverage] = useState<OntologyCoverage | null>(null);
  const [drafts, setDrafts] = useState<OntologyDraft[]>([]);
  const [draftCursor, setDraftCursor] = useState("");
  const [draft, setDraft] = useState<OntologyDraft | null>(null);
  const [edits, setEdits] = useState<OntologyEdit[]>([]);
  const [reason, setReason] = useState("");
  const [dirty, setDirty] = useState(false);
  const [preview, setPreview] = useState<OntologyPreview | null>(null);
  const [publications, setPublications] = useState<OntologyPublication[]>([]);
  const [historyCursor, setHistoryCursor] = useState(0);
  const [publication, setPublication] = useState<OntologyPublication | null>(null);
  const [rollbackReason, setRollbackReason] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [fresh, setFresh] = useState(false);
  const operationKeys = useRef(new Map<string, string>());
  const canWrite = session.membership.grants.includes("write");
  const open = !draft || draft.state === "open";
  const stale = Boolean(draft && coverage && draft.base_revision !== coverage.revision);

  function operationKey(action: string, body: unknown) {
    const signature = JSON.stringify([action, draft?.id ?? "", body]);
    let value = operationKeys.current.get(signature);
    if (!value) { value = newIdempotencyKey("ontology"); operationKeys.current.set(signature, value); }
    return value;
  }

  async function load() {
    setPreview(null); setFresh(false);
    const [catalog, status, saved, history] = await Promise.all([api.ontologyRecords(recordKind), api.ontologyStatus(), api.ontologyDrafts(), api.ontologyPublications()]);
    setRecords(catalog.records); setRecordCursor(catalog.next_cursor ?? ""); setCoverage(status);
    setDrafts(saved.drafts); setDraftCursor(saved.next_cursor ?? ""); setPublications(history.publications); setHistoryCursor(history.next_revision ?? 0); setFresh(true);
  }

  useEffect(() => {
    let active = true;
    Promise.all([api.ontologyRecords(), api.ontologyStatus(), api.ontologyDrafts(), api.ontologyPublications(), api.ontologySources()])
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

  function add(edit: OntologyEdit) { setEdits((current) => [...current, edit]); setDirty(true); setPreview(null); setMessage("Correction added. Save the draft to validate it."); }
  function startDraft() { operationKeys.current.clear(); setDraft(null); setEdits([]); setReason(""); setDirty(false); setPreview(null); setTarget(null); setSelected([]); setMessage(""); }

  async function openDraft(id: string) {
    const next = await api.ontologyDraft(id); setDraft(next); setEdits(next.edits); setReason(next.reason); setDirty(false); setPreview(null); setTarget(null); setSelected([]);
  }

  async function save() {
    const body = { expected_version: draft?.version ?? 0, expected_revision: draft?.base_revision ?? coverage!.revision, reason, edits };
    const next = await api.saveOntologyDraft(draft?.id ?? "", { ...body, operation_key: operationKey("save", body) });
    setDraft(next); setDirty(false); setPreview(null); setDrafts((current) => [next, ...current.filter((item) => item.id !== next.id)]); setMessage("Draft saved. Preview its impact before publishing.");
  }

  async function refreshDraft() {
    const status = await api.ontologyStatus();
    const nextEdits = await Promise.all(edits.map(async (edit) => {
      const next = { ...edit };
      if (edit.record_id) next.expected_version = (await api.ontologyRecord(edit.record_id)).version;
      const refreshSource = async (source: SourceHandle) => {
        const found = (await api.ontologySources(source.kind, "", source.id)).sources.find((item) => item.id === source.id);
        if (!found) throw new Error("A selected source is unavailable. Remove or replace that correction.");
        return { kind: found.kind, id: found.id, version: found.version };
      };
      if (edit.source) next.source = await refreshSource(edit.source);
      if (edit.members) next.members = await Promise.all(edit.members.map(refreshSource));
      return next;
    }));
    if (draft) { const saved = await api.ontologyDraft(draft.id); setDraft({ ...saved, base_revision: status.revision }); }
    setEdits(nextEdits); setCoverage(status); setDirty(true); setPreview(null); setFresh(true); setMessage("Draft refreshed. Review the corrections, save, and preview again.");
  }

  async function command(action: "publish" | "discard") {
    const body = { expected_version: draft!.version, expected_revision: draft!.base_revision, reason };
    const next = await api.commandOntologyDraft(draft!.id, action, { ...body, operation_key: operationKey(action, body) });
    setDraft(next); setDirty(false); setPreview(null);
    setMessage(action === "publish" ? "Correction published. Affected organization is waiting for reassessment." : "Draft discarded. Its history is retained.");
    await load();
  }

  async function catalog(kind: OntologyKind | "", more = false) {
    const page = await api.ontologyRecords(kind, more ? recordCursor : "");
    setRecords((current) => more ? [...current, ...page.records] : page.records); setRecordCursor(page.next_cursor ?? ""); setRecordKind(kind); setPreview(null);
  }

  async function sourcePage(more = false) {
    const page = await api.ontologySources(sourceKind, more ? sourceCursor : "", sourceQuery);
    setSources((current) => more ? [...current, ...page.sources] : page.sources); setSourceCursor(page.next_cursor ?? "");
  }

  async function rollback() {
    const body = { expected_revision: coverage!.revision, reason: rollbackReason };
    const signature = JSON.stringify(["rollback", publication!.id, body]);
    let key = operationKeys.current.get(signature); if (!key) { key = newIdempotencyKey("ontology"); operationKeys.current.set(signature, key); }
    await api.rollbackOntologyPublication(publication!.id, { ...body, operation_key: key });
    setPublication(null); setRollbackReason(""); setMessage("Rollback published as a new revision. Affected organization is waiting for reassessment."); await load();
  }

  if (loading) return <LoadingState label="Loading ontology" />;
  return <div className="stack">
    <SectionHeading title="Ontology" subtitle="Inspect shared organization and publish reversible corrections." actions={<button className="ghost-button" disabled={busy} onClick={() => void perform(load)}>Refresh ontology</button>} />
    {error && <p className="field-error" role="alert">{error}</p>}{message && <p role="status">{message}</p>}
    {coverage && <section className="surface-section" aria-label="Team ontology coverage"><h3>Team processing coverage</h3><p>Revision {coverage.revision}. {coverage.coverage_complete ? "Current" : "Reassessment pending"}. {coverage.counts.organized} organized; {coverage.counts.pending} pending; {coverage.counts.ambiguous} ambiguous; {coverage.counts.failed} failed; {coverage.counts.budget_deferred} deferred.</p><p>Processing runs under the operator’s maintenance schedule. Refresh to check progress.</p></section>}
    <section className="surface-section" aria-label="Ontology catalog"><h3>Catalog and groups</h3>
      <label>Catalog type<select aria-label="Catalog type" value={recordKind} disabled={busy} onChange={(event) => void perform(() => catalog(event.target.value as OntologyKind | ""))}><option value="">All records</option>{["topic", "entity_class", "predicate_concept", "assignment", "evidence_group", "relationship_group", "override"].map((kind) => <option key={kind} value={kind}>{kind.replaceAll("_", " ")}</option>)}</select></label>
      <div className="table-wrap"><table className="data-table"><thead><tr><th>Organization</th><th>Status</th><th>Sources or classification</th><th>Actions</th></tr></thead><tbody>{records.map((record) => <tr key={record.id}>
        <td><strong>{recordLabel(record)}</strong>{record.definition && <p>{record.definition.description}</p>}<small title={record.id}>{record.id.slice(0, 8)} · revision {record.version}</small></td>
        <td>{record.retired ? "Retired" : record.current ? "Current" : "Needs reassessment"}</td>
        <td><RecordDetails record={record} records={records} /></td>
        <td>{!record.retired && record.definition && <button disabled={busy || !canWrite} onClick={() => { setTarget(record); setMessage("Definition selected. Edit its fields below."); }}>Edit {record.definition.label}</button>}
          {!record.retired && record.group && <button disabled={busy || !canWrite} onClick={() => { setTarget(record); setSelected(record.group!.members); }}>Correct grouping</button>}
          {!record.retired && record.override && <button disabled={busy || !canWrite} onClick={() => add({ action: "remove_override", record_id: record.id, expected_version: record.version })}>{record.override.action === "pin_definition" ? "Unpin definition" : "Remove correction"}</button>}
        </td></tr>)}</tbody></table></div>
      {!records.length && <p>No catalog records yet.</p>}{recordCursor && <button disabled={busy} onClick={() => void perform(() => catalog(recordKind, true))}>Load more catalog records</button>}
    </section>
    <section className="surface-section"><h3>Shared sources</h3>
      <label>Source type<select aria-label="Source type" value={sourceKind} disabled={busy} onChange={(event) => { setSourceKind(event.target.value as SourceKind); setSources([]); setSourceCursor(""); }}>{["evidence", "relationship", "entity", "predicate"].map((kind) => <option key={kind}>{kind}</option>)}</select></label>
      <label>Source text or ID<input aria-label="Source text or ID" value={sourceQuery} maxLength={128} onChange={(event) => setSourceQuery(event.target.value)} /></label><button disabled={busy} onClick={() => void perform(() => sourcePage())}>Inspect sources</button>
      {sources.map((source) => <label className="checkbox-row" key={sourceKey(source)}><input type="checkbox" aria-label={`Select source ${source.id}`} disabled={busy || !canWrite || !open} checked={selected.some((item) => sourceKey(item) === sourceKey(source))} onChange={(event) => setSelected((current) => event.target.checked ? [...current, { kind: source.kind, id: source.id, version: source.version }] : current.filter((item) => sourceKey(item) !== sourceKey(source)))} /><span>{source.text}{source.truncated && " … (excerpt)"}</span></label>)}
      {sourceCursor && <button disabled={busy} onClick={() => void perform(() => sourcePage(true))}>Load more sources</button>}<p>Text filtering applies to each inspected page. Load more sources to continue searching. A source ID looks up that shared source directly.</p><p>{selected.length} selected sources.</p>
      {!!selected.length && <button disabled={busy} onClick={() => setSelected([])}>Clear selected sources</button>}
    </section>
    <section className="surface-section"><SectionHeading title="Saved drafts" subtitle="Only your drafts are listed." actions={<button disabled={busy || !canWrite} onClick={startDraft}>New draft</button>} />
      {drafts.map((item) => <button key={item.id} disabled={busy} onClick={() => void perform(() => openDraft(item.id))}>{item.reason} · {item.state} · revision {item.version}</button>)}
      {draftCursor && <button disabled={busy} onClick={() => void perform(async () => { const page = await api.ontologyDrafts(draftCursor); setDrafts((current) => [...current, ...page.drafts]); setDraftCursor(page.next_cursor ?? ""); })}>Load more drafts</button>}
      <h3>{draft ? `Draft ${draft.id.slice(0, 8)} · ${draft.state}` : "New correction draft"}</h3>
      {stale && <p role="alert">This draft uses an older catalog revision. Refresh it before publishing.</p>}
      <label>Audit reason<textarea aria-label="Audit reason" value={reason} maxLength={512} disabled={busy || !canWrite || !open} onChange={(event) => { setReason(event.target.value); setDirty(true); setPreview(null); }} /></label>
      <OntologyEditor records={records} selected={selected} target={target} disabled={busy || !canWrite || !open || edits.length >= 64} onAdd={add} />
      <ol>{edits.map((edit, index) => <li key={index}><EditDetails edit={edit} records={records} />{open && <button aria-label={`Remove correction ${index + 1}`} disabled={busy || !canWrite} onClick={() => { setEdits((current) => current.filter((_, position) => position !== index)); setDirty(true); setPreview(null); }}>Remove</button>}</li>)}</ol>
      <button className="primary-button" disabled={busy || !canWrite || !open || !fresh || !reason.trim() || !edits.length || Boolean(draft && !dirty)} onClick={() => void perform(save)}>Save draft</button>
      {draft?.state === "open" && <>
        <button disabled={busy || dirty || stale || !fresh} onClick={() => void perform(async () => { setPreview(await api.previewOntologyDraft(draft.id, draft.version)); })}>Preview impact</button>
        <button disabled={busy || !canWrite} onClick={() => void perform(refreshDraft)}>Refresh draft before resubmitting</button>
        <button disabled={busy || !canWrite || dirty || !reason.trim()} onClick={() => void perform(() => command("discard"))}>Discard draft</button>
      </>}
      {preview && <div aria-label="Impact preview"><h3>Impact preview</h3><p>{preview.changed_records} changes; {preview.affected_records} affected existing records; {preview.affected_sources} currently available sources.</p>
        <ul>{preview.examples.map((source) => <li key={sourceKey(source)}>{source.text}{source.truncated && " … (excerpt)"}</li>)}</ul>{preview.examples_truncated && <p>Examples are truncated; counts cover the stored dependency set.</p>}
        {preview.requires_reassessment && <p>Publication invalidates affected organization immediately. Automatic reassessment may classify additional related sources later.</p>}
        <button className="primary-button" disabled={busy || !canWrite || !preview.complete || dirty || stale || !fresh} onClick={() => void perform(() => command("publish"))}>Publish correction</button>
      </div>}
    </section>
    <section className="surface-section"><h3>Publication history</h3>{publications.map((item) => <button key={item.id} disabled={busy} onClick={() => void perform(async () => { setPublication(await api.ontologyPublication(item.id)); setRollbackReason(""); })}>Revision {item.revision} · {item.origin} · {item.reason}</button>)}
      {historyCursor > 0 && <button disabled={busy} onClick={() => void perform(async () => { const page = await api.ontologyPublications(historyCursor); setPublications((current) => [...current, ...page.publications]); setHistoryCursor(page.next_revision ?? 0); })}>Load more publications</button>}
      {publication && <div aria-label="Publication detail"><h3>Publication revision {publication.revision}</h3><p>{publication.reason}</p><ul>{publication.record_views?.map((record) => <li key={record.id}>{recordLabel(record)} · revision {record.version} · {record.current ? "Current" : "Historical or awaiting reassessment"}<RecordDetails record={record} records={publication.record_views ?? []} /></li>)}</ul>
        {(publication.origin === "manager" || publication.origin === "rollback") && <><label>Rollback reason<textarea aria-label="Rollback reason" value={rollbackReason} maxLength={512} disabled={busy || !canWrite} onChange={(event) => setRollbackReason(event.target.value)} /></label><button disabled={busy || !canWrite || !fresh || !rollbackReason.trim()} onClick={() => void perform(rollback)}>Publish rollback</button><p>Rollback creates a new revision and is rejected if the changed records or their sources are no longer eligible.</p></>}
      </div>}
    </section>
  </div>;
}

function readError(error: unknown) { return error instanceof Error ? error.message : "Ontology request failed."; }

function definitionLabel(id: string, records: OntologyRecord[]) {
  const label = records.find((record) => record.id === id)?.definition?.label;
  return label ? `${label} (${id})` : id;
}

function SourceDetails({ source }: { source: SourceHandle }) {
  return <span>{source.kind} {source.id} · version {source.version}</span>;
}

function RecordDetails({ record, records }: { record: OntologyRecord; records: OntologyRecord[] }) {
  return <div>
    {record.definition && <><p>Key: {record.definition.key}</p><p>Aliases: {record.definition.aliases?.join(", ") || "None"}</p>{record.definition.parent_id && <p>Parent: {definitionLabel(record.definition.parent_id, records)}</p>}</>}
    {record.assignment && <p><SourceDetails source={record.assignment.source} /> → {definitionLabel(record.assignment.definition_id, records)}</p>}
    {record.override?.target_id && <p>Target: {definitionLabel(record.override.target_id, records)}</p>}
    {record.override?.definition_id && <p>Classification: {definitionLabel(record.override.definition_id, records)}</p>}
    {(record.group?.members ?? record.override?.members)?.map((member) => <p key={sourceKey(member)}><SourceDetails source={member} /></p>)}
  </div>;
}

function EditDetails({ edit, records }: { edit: OntologyEdit; records: OntologyRecord[] }) {
  return <div>{edit.action.replaceAll("_", " ")}
    {edit.definition && <p>{edit.definition.label} · {edit.definition.description} · aliases: {edit.definition.aliases?.join(", ") || "None"}{edit.definition.parent_id && ` · parent: ${definitionLabel(edit.definition.parent_id, records)}`}</p>}
    {edit.record_id && <p>Target record: {edit.record_id} · expected version {edit.expected_version}</p>}
    {edit.definition_id && <p>Definition: {definitionLabel(edit.definition_id, records)}</p>}
    {edit.source && <p><SourceDetails source={edit.source} /></p>}
    {edit.members?.map((member) => <p key={sourceKey(member)}><SourceDetails source={member} /></p>)}
  </div>;
}
