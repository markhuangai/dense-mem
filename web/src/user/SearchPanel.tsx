import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { FileText, Search } from "lucide-react";
import { Button } from "../ui/primitives/button";
import { Label } from "../ui/primitives/label";
import { Input } from "../ui/primitives/input";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { LoadingState } from "../ui/components";
import { ResponsiveInspector, useCompactWorkspace } from "../ui/inspector";
import { RecallEvidenceContext, RecallPayload, RelatedTopic, UserApi } from "./api";

export function SearchPanel({ api }: { api: UserApi }) {
  const sequence = useRef(0);
  useEffect(() => () => { sequence.current += 1; }, [api]);
  const [query, setQuery] = useState("");
  const [payload, setPayload] = useState<RecallPayload | null>(null);
  const [topic, setTopic] = useState<RelatedTopic | null>(null);
  const [selectedID, setSelectedID] = useState("");
  const [source, setSource] = useState("all");
  const [sort, setSort] = useState("relevance");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const { ref, compact } = useCompactWorkspace();

  async function recall(selectedTopic: RelatedTopic | null) {
    const text = selectedTopic ? "" : query.trim();
    if (!text && !selectedTopic) { setError("Query is required."); return; }
    const request = ++sequence.current;
    setLoading(true);
    setError("");
    try {
      const result = await api.recall(text, 10, selectedTopic ? { topicIDs: [selectedTopic.topic_id] } : {});
      if (request !== sequence.current) return;
      setPayload(result);
      setTopic(selectedTopic);
      setSelectedID(result.results[0]?.evidence_id ?? "");
      setSource("all");
      setInspectorOpen(!compact);
    } catch (cause) {
      if (request === sequence.current) setError(cause instanceof Error ? cause.message : "Recall failed.");
    } finally {
      if (request === sequence.current) setLoading(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) { event.preventDefault(); void recall(null); }
  const sources = useMemo(() => [...new Set((payload?.results ?? []).map(sourceLabel))].sort(), [payload]);
  const results = useMemo(() => {
    const items = (payload?.results ?? []).filter((item) => source === "all" || sourceLabel(item) === source);
    return sort === "date" ? [...items].sort((a, b) => (b.created_at ?? "").localeCompare(a.created_at ?? "")) : items;
  }, [payload, source, sort]);
  const selected = results.find((item) => item.evidence_id === selectedID) ?? results[0];

  return <section ref={ref} className={inspectorOpen && !compact ? "knowledge-explorer has-inspector" : "knowledge-explorer"} aria-label="Knowledge explorer">
    <section className="knowledge-results-panel" aria-label="Recall results">
      <form className="knowledge-commandbar" onSubmit={submit}>
        <Label className="sr-only" htmlFor="recall-query">Keyword</Label>
        <div className="search-input-wrap large"><Search size={17} aria-hidden="true" /><Input id="recall-query" aria-label="Keyword" placeholder="Search evidence..." value={query} onChange={(event) => setQuery(event.target.value)} /></div>
        <Button type="submit" disabled={loading}><Search size={16} aria-hidden="true" />Search</Button>
      </form>
      <div className="knowledge-results-toolbar">
        <div><h2>{results.length} results</h2><span>{topic ? `Topic: ${topic.name}` : "Search memory Evidence"}</span></div>
        <div className="toolbar-actions">
          <NativeSelect aria-label="Source" value={source} onChange={(event) => setSource(event.target.value)}><NativeSelectOption value="all">All sources</NativeSelectOption>{sources.map((value) => <NativeSelectOption key={value} value={value}>{value}</NativeSelectOption>)}</NativeSelect>
          <NativeSelect aria-label="Sort" value={sort} onChange={(event) => setSort(event.target.value)}><NativeSelectOption value="relevance">Relevance</NativeSelectOption><NativeSelectOption value="date">Date</NativeSelectOption></NativeSelect>
          {!inspectorOpen && <Button variant="outline" type="button" disabled={!selected} onClick={() => setInspectorOpen(true)}>Open details</Button>}
        </div>
      </div>
      {error && <div className="banner error" role="alert">{error}</div>}
      {payload?.degradations.map((item, index) => <div className="banner warning" role="status" key={`${item.code}-${index}`}>{item.message}</div>)}
      {payload && payload.conflicts.length > 0 && <div className="banner warning" role="status">{payload.conflicts.length} Evidence conflicts are included in this recall.</div>}
      {loading ? <LoadingState label="Searching evidence" /> : results.length === 0 ? <div className="table-placeholder">No recall results</div> :
        <div className="knowledge-list compact-list" role="listbox" aria-label="Recall result list">{results.map((item) =>
          <article key={item.evidence_id} role="option" aria-selected={item.evidence_id === selected?.evidence_id} tabIndex={0}
            className={item.evidence_id === selected?.evidence_id ? "knowledge-item knowledge-result-option selected" : "knowledge-item knowledge-result-option"}
            onClick={() => { setSelectedID(item.evidence_id); setInspectorOpen(true); }}
            onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); setSelectedID(item.evidence_id); setInspectorOpen(true); } }}>
            <div className="result-kicker"><FileText size={15} aria-hidden="true" /><span className="status-pill neutral">Evidence</span></div>
            <p>{item.context}</p><div className="result-meta-row"><span>Source: {sourceLabel(item)}</span><time>{item.created_at ? new Date(item.created_at).toLocaleString() : "Unknown date"}</time></div>
          </article>)}</div>}
      {payload && payload.related_topics.length > 0 && <section className="inspector-section" aria-label="Related topics"><h3>Related topics</h3>{payload.related_topics.map((item) =>
        <div key={item.topic_id}><Button variant="outline" type="button" disabled={loading} onClick={() => void recall(item)}>{item.name} · {item.eligible_evidence_count} Evidence</Button><p>{item.description}</p></div>)}</section>}
      {payload?.topics_truncated && <p role="status">More related topics are available.</p>}
    </section>
    <ResponsiveInspector compact={compact} open={inspectorOpen} onOpenChange={setInspectorOpen} title="Evidence details">
      {selected ? <div className="inspector-card"><h3>Evidence</h3><p>{selected.context}</p><dl className="evidence-list">
        <div><dt>ID</dt><dd>{selected.evidence_id}</dd></div><div><dt>Source</dt><dd>{sourceLabel(selected)}</dd></div>
        <div><dt>Destination</dt><dd>{selected.space_kind ?? "Unknown"}</dd></div><div><dt>Rank</dt><dd>{selected.rank}</dd></div>
        <div><dt>Equivalent Evidence</dt><dd>{selected.equivalent_evidence_ids.join(", ") || "None"}{selected.equivalents_truncated ? " (truncated)" : ""}</dd></div>
      </dl></div> : <div className="table-placeholder compact">Select a result</div>}
    </ResponsiveInspector>
  </section>;
}

function sourceLabel(item: RecallEvidenceContext) { return item.source || item.source_type || "Unknown source"; }
