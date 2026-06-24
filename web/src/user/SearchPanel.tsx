import { CSSProperties, FormEvent, useMemo, useState } from "react";
import {
  Bookmark,
  ChevronDown,
  ExternalLink,
  FileText,
  GitBranch,
  MoreVertical,
  Plus,
  Search,
  ShieldCheck,
  Star,
  X,
} from "lucide-react";
import { LoadingState, SectionHeading } from "../ui/components";
import { Claim, Community, Fact, Fragment, RecallHit, UserApi } from "./api";

type RecallResultKind = "fact" | "claim" | "fragment";
type RecallResultStatus = "verified" | "provisional" | "disputed" | "deprecated";
type RecallSortMode = "relevance" | "date";
type ResultDensity = "comfortable" | "compact";
type InspectorTab = "evidence" | "lineage" | "recall";

type IndexedRecallResult = {
  hit: RecallHit;
  key: string;
  kind: RecallResultKind;
};

export function SearchPanel({ api }: { api: UserApi }) {
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<RecallHit[]>([]);
  const [communities, setCommunities] = useState<Community[]>([]);
  const [selectedKey, setSelectedKey] = useState("");
  const [enabledTypes, setEnabledTypes] = useState<Record<RecallResultKind, boolean>>({
    fact: true,
    claim: true,
    fragment: true,
  });
  const [enabledStatuses, setEnabledStatuses] = useState<Record<RecallResultStatus, boolean>>({
    verified: true,
    provisional: true,
    disputed: false,
    deprecated: false,
  });
  const [dateMode, setDateMode] = useState<"all" | "custom">("all");
  const [startDate, setStartDate] = useState("");
  const [endDate, setEndDate] = useState("");
  const [sourceFilter, setSourceFilter] = useState("all");
  const [sortMode, setSortMode] = useState<RecallSortMode>("relevance");
  const [density, setDensity] = useState<ResultDensity>("comfortable");
  const [inspectorOpen, setInspectorOpen] = useState(true);
  const [inspectorTab, setInspectorTab] = useState<InspectorTab>("evidence");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = query.trim();
    if (!trimmed) {
      setError("Query is required.");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const [nextHits, nextCommunities] = await Promise.all([
        api.recall(trimmed, 10),
        api.listCommunities(20).catch(() => ({ items: [] as Community[] })),
      ]);
      setHits(nextHits);
      setCommunities(nextCommunities.items);
      setSourceFilter("all");
      setInspectorOpen(true);
      setInspectorTab("evidence");
      setSelectedKey(nextHits[0] ? recallKey(nextHits[0], 0) : "");
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  const entities = useMemo(() => deriveEntities(hits, communities), [hits, communities]);
  const indexedHits = useMemo(() => hits.map((hit, index) => ({
    hit,
    key: recallKey(hit, index),
    kind: recallResultKind(hit),
  })), [hits]);
  const filteredHits = useMemo(() => (
    indexedHits.filter((indexed) => {
      const item = resultItem(indexed.hit);
      const status = recallResultStatus(item);
      return enabledTypes[indexed.kind]
        && enabledStatuses[status]
        && sourceMatches(item, sourceFilter)
        && dateMatches(item, dateMode, startDate, endDate);
    })
  ), [indexedHits, enabledTypes, enabledStatuses, sourceFilter, dateMode, startDate, endDate]);
  const sortedHits = useMemo(() => sortRecallResults(filteredHits, sortMode), [filteredHits, sortMode]);
  const selectedResult = useMemo(() => (
    sortedHits.find((item) => item.key === selectedKey) ?? sortedHits[0] ?? null
  ), [sortedHits, selectedKey]);
  const typeCounts = useMemo(() => indexedHits.reduce<Record<RecallResultKind, number>>((counts, item) => ({
    ...counts,
    [item.kind]: counts[item.kind] + 1,
  }), { fact: 0, claim: 0, fragment: 0 }), [indexedHits]);
  const statusCounts = useMemo(() => indexedHits.reduce<Record<RecallResultStatus, number>>((counts, indexed) => {
    const status = recallResultStatus(resultItem(indexed.hit));
    return {
      ...counts,
      [status]: counts[status] + 1,
    };
  }, { verified: 0, provisional: 0, disputed: 0, deprecated: 0 }), [indexedHits]);
  const sourceOptions = useMemo(() => {
    const values = new Set<string>();
    for (const hit of indexedHits) {
      values.add(sourceLabel(resultItem(hit.hit)));
    }
    return Array.from(values).sort((left, right) => left.localeCompare(right));
  }, [indexedHits]);

  function toggleType(kind: RecallResultKind) {
    setEnabledTypes((current) => ({
      ...current,
      [kind]: !current[kind],
    }));
  }

  function toggleStatus(status: RecallResultStatus) {
    setEnabledStatuses((current) => ({
      ...current,
      [status]: !current[status],
    }));
  }

  function clearFilters() {
    setEnabledTypes({ fact: true, claim: true, fragment: true });
    setEnabledStatuses({ verified: true, provisional: true, disputed: false, deprecated: false });
    setDateMode("all");
    setStartDate("");
    setEndDate("");
    setSourceFilter("all");
  }

  return (
    <section className={inspectorOpen ? "knowledge-explorer" : "knowledge-explorer inspector-closed"} aria-label="Knowledge explorer">
      <aside className="knowledge-filter-panel" aria-label="Knowledge filters">
        <SectionHeading
          title="Filters"
          actions={(
            <button
              className="text-button"
              type="button"
              onClick={clearFilters}
            >
              Clear all
            </button>
          )}
        />
        <fieldset className="filter-stack">
          <legend>Type</legend>
          {(["fact", "claim", "fragment"] as RecallResultKind[]).map((kind) => (
            <label className="filter-row" key={kind}>
              <input
                type="checkbox"
                checked={enabledTypes[kind]}
                onChange={() => toggleType(kind)}
              />
              <span>{recallResultKindLabel(kind)}</span>
              <small>{typeCounts[kind]}</small>
            </label>
          ))}
        </fieldset>
        <fieldset className="filter-stack">
          <legend>Status</legend>
          {(["verified", "provisional", "disputed", "deprecated"] as RecallResultStatus[]).map((status) => (
            <label className="filter-row" key={status}>
              <input type="checkbox" checked={enabledStatuses[status]} onChange={() => toggleStatus(status)} />
              <span>{recallResultStatusLabel(status)}</span>
              <small>{statusCounts[status]}</small>
            </label>
          ))}
        </fieldset>
        <div className="filter-stack">
          <strong>Date</strong>
          <select aria-label="Date range" value={dateMode} onChange={(event) => setDateMode(event.target.value as "all" | "custom")}>
            <option value="all">All time</option>
            <option value="custom">Custom</option>
          </select>
          {dateMode === "custom" && (
            <>
              <input aria-label="Start date" type="date" value={startDate} onChange={(event) => setStartDate(event.target.value)} />
              <input aria-label="End date" type="date" value={endDate} onChange={(event) => setEndDate(event.target.value)} />
            </>
          )}
        </div>
        <div className="filter-stack">
          <strong>Source</strong>
          <select aria-label="Source" value={sourceFilter} onChange={(event) => setSourceFilter(event.target.value)}>
            <option value="all">All sources</option>
            {sourceOptions.map((source) => <option key={source} value={source}>{source}</option>)}
          </select>
        </div>
        {entities.length > 0 && (
          <div className="filter-stack" aria-label="Related entities">
            <strong>Tag</strong>
            <div className="entity-strip">
              {entities.map((entity) => <span className="status-pill neutral" key={entity}>{entity}</span>)}
            </div>
          </div>
        )}
      </aside>

      <section className="knowledge-results-panel" aria-label="Recall results">
        <form className="knowledge-commandbar" onSubmit={submit}>
          <label className="sr-only" htmlFor="recall-query">Keyword</label>
          <div className="search-input-wrap large">
            <Search size={17} aria-hidden="true" />
            <input
              id="recall-query"
              aria-label="Keyword"
              placeholder="Search knowledge..."
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <button className="primary-button compact" type="submit" disabled={loading}>
            <Search size={16} aria-hidden="true" />
            Search
          </button>
        </form>
        <div className="knowledge-results-toolbar">
          <div>
            <h2>{filteredHits.length.toLocaleString()} results</h2>
            <span>{query.trim() ? `for "${query.trim()}"` : "Search across facts, claims, and memory"}</span>
          </div>
          <div className="toolbar-actions">
            <button
              className="select-like compact"
              type="button"
              aria-label={`Sort by ${sortMode === "relevance" ? "date" : "relevance"}`}
              onClick={() => setSortMode((current) => (current === "relevance" ? "date" : "relevance"))}
            >
              Sort: {sortMode === "relevance" ? "Relevance" : "Date"} <ChevronDown size={14} aria-hidden="true" />
            </button>
            <button
              className={density === "compact" ? "icon-button active" : "icon-button"}
              type="button"
              aria-label={density === "compact" ? "Use comfortable density" : "Use compact density"}
              aria-pressed={density === "compact"}
              onClick={() => setDensity((current) => (current === "comfortable" ? "compact" : "comfortable"))}
            >
              <FileText size={16} aria-hidden="true" />
            </button>
            {!inspectorOpen && (
              <button className="ghost-button compact" type="button" onClick={() => setInspectorOpen(true)}>
                <FileText size={15} aria-hidden="true" />
                Open details
              </button>
            )}
          </div>
        </div>
        {error && <div className="banner error" role="alert">{error}</div>}
        {loading && <LoadingState label="Searching knowledge" />}
        {!loading && (
          <RecallResults
            items={sortedHits}
            selectedKey={selectedResult?.key ?? ""}
            onSelect={setSelectedKey}
            density={density}
          />
        )}
      </section>

      {inspectorOpen && (
        <aside className="knowledge-inspector" aria-label="Inspector">
          <div className="inspector-head">
            <h2>Inspector</h2>
            <button className="icon-button" type="button" aria-label="Close details panel" onClick={() => setInspectorOpen(false)}><X size={16} aria-hidden="true" /></button>
          </div>
          <KnowledgeInspector result={selectedResult} activeTab={inspectorTab} onSelectTab={setInspectorTab} />
        </aside>
      )}
    </section>
  );
}

function RecallResults({
  items,
  selectedKey,
  onSelect,
  density,
}: {
  items: IndexedRecallResult[];
  selectedKey: string;
  onSelect: (key: string) => void;
  density: ResultDensity;
}) {
  if (items.length === 0) {
    return <div className="table-placeholder">No recall results</div>;
  }
  return (
    <div className={density === "compact" ? "knowledge-list compact-list dense" : "knowledge-list compact-list"} role="listbox" aria-label="Recall result list">
      {items.map((result) => {
        const item = resultItem(result.hit);
        const status = recallResultStatus(item);
        return (
          <article
            aria-selected={result.key === selectedKey}
            className={
              result.key === selectedKey
                ? "knowledge-item knowledge-result-option selected"
                : "knowledge-item knowledge-result-option"
            }
            key={result.key}
            onClick={() => onSelect(result.key)}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                onSelect(result.key);
              }
            }}
            role="option"
            tabIndex={0}
          >
            <div className="knowledge-item-head">
              <div className="result-kicker">
                {resultIcon(result.kind)}
                <span className="status-pill neutral">{recallResultKindLabel(result.kind)}</span>
                <span className={statusPillClass(status)}>{recallResultStatusLabel(status)}</span>
              </div>
              <div className="result-actions">
                <button className="icon-button bare" type="button" aria-label={`Star ${itemTitle(item)}`}>
                  <Star size={15} aria-hidden="true" />
                </button>
                <button className="icon-button bare" type="button" aria-label={`More actions ${itemTitle(item)}`}>
                  <MoreVertical size={15} aria-hidden="true" />
                </button>
              </div>
            </div>
            <h3>{itemTitle(item)}</h3>
            <p>{itemBody(item)}</p>
            <div className="result-meta-row">
              <span>Source: {sourceLabel(item)}</span>
              <time>{itemDate(item)}</time>
            </div>
          </article>
        );
      })}
    </div>
  );
}

function KnowledgeInspector({
  result,
  activeTab,
  onSelectTab,
}: {
  result: IndexedRecallResult | null;
  activeTab: InspectorTab;
  onSelectTab: (tab: InspectorTab) => void;
}) {
  if (!result) {
    return <div className="table-placeholder compact">Select a result</div>;
  }

  const item = resultItem(result.hit);
  const status = recallResultStatus(item);
  const tabs: Array<{ id: InspectorTab; label: string }> = [
    { id: "evidence", label: "Evidence" },
    { id: "lineage", label: "Lineage" },
    { id: "recall", label: "Recall" },
  ];
  return (
    <article className="inspector-card">
      <div className="inspector-tabs" role="tablist" aria-label="Result sections">
        {tabs.map((tab) => (
          <button
            className={tab.id === activeTab ? "inspector-tab active" : "inspector-tab"}
            type="button"
            role="tab"
            aria-selected={tab.id === activeTab}
            key={tab.id}
            onClick={() => onSelectTab(tab.id)}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div className="inspector-status-row">
        <span className="status-pill neutral">{recallResultKindLabel(result.kind)}</span>
        <span className={statusPillClass(status)}>{recallResultStatusLabel(status)}</span>
      </div>
      <h3>{itemTitle(item)}</h3>
      {activeTab === "evidence" && (
        <div className="inspector-section" role="tabpanel">
          <h4>Evidence</h4>
          <p>{itemBody(item)}</p>
          <dl className="evidence-list">
            <div>
              <dt>Source</dt>
              <dd>
                {sourceLabel(item)}
                <ExternalLink size={13} aria-hidden="true" />
              </dd>
            </div>
            <div>
              <dt>Collected</dt>
              <dd>{itemDate(item)}</dd>
            </div>
            <div>
              <dt>Confidence</dt>
              <dd>
                <span>{scoreLabel(result.hit.score ?? result.hit.final_score) || "n/a"}</span>
                <span className="confidence-bar" style={{ "--confidence": confidencePercent(result.hit) } as CSSProperties} />
              </dd>
            </div>
          </dl>
        </div>
      )}
      {activeTab === "lineage" && (
        <div className="inspector-section" role="tabpanel">
          <h4>Lineage</h4>
          <dl className="evidence-list">
            <div>
              <dt>Derived from</dt>
              <dd>{sourceLabel(item)}</dd>
            </div>
            <div>
              <dt>Recorded</dt>
              <dd>{itemDate(item)}</dd>
            </div>
            <div>
              <dt>Status</dt>
              <dd>{recallResultStatusLabel(status)}</dd>
            </div>
          </dl>
        </div>
      )}
      {activeTab === "recall" && (
        <div className="inspector-section" role="tabpanel">
          <h4>Recall</h4>
          <dl className="evidence-list">
            <div>
              <dt>Final score</dt>
              <dd>{scoreLabel(result.hit.final_score ?? result.hit.score) || "n/a"}</dd>
            </div>
            <div>
              <dt>Semantic rank</dt>
              <dd>{rankLabel(result.hit.semantic_rank)}</dd>
            </div>
            <div>
              <dt>Keyword rank</dt>
              <dd>{rankLabel(result.hit.keyword_rank)}</dd>
            </div>
          </dl>
        </div>
      )}
      <dl className="inspector-details">
        <div>
          <dt>Tier</dt>
          <dd>{tierLabel(result.hit)}</dd>
        </div>
        <div>
          <dt>Type</dt>
          <dd>{recallResultKindLabel(result.kind)}</dd>
        </div>
      </dl>
      <div className="inspector-actions">
        <button className="ghost-button compact" type="button">
          <Bookmark size={15} aria-hidden="true" />
          Add to collection
        </button>
        <button className="ghost-button compact" type="button">
          <Plus size={15} aria-hidden="true" />
          Create claim
        </button>
      </div>
    </article>
  );
}

function deriveEntities(hits: RecallHit[], communities: Community[]): string[] {
  const values = new Set<string>();
  for (const hit of hits) {
    const source = hit.fact ?? hit.claim;
    if (source) {
      addEntity(values, source.subject);
      addEntity(values, source.object);
      addEntity(values, source.predicate);
    }
  }
  for (const community of communities) {
    for (const entity of community.top_entities ?? []) {
      addEntity(values, entity);
    }
  }
  return Array.from(values).slice(0, 20);
}

function addEntity(values: Set<string>, value: string | undefined) {
  const trimmed = value?.trim();
  if (trimmed) {
    values.add(trimmed);
  }
}

function itemTitle(item: Fact | Claim | Fragment | undefined): string {
  if (!item) {
    return "Result";
  }
  if ("content" in item) {
    return item.source || shortId(item.fragment_id || item.id);
  }
  return item.subject;
}

function itemBody(item: Fact | Claim | Fragment | undefined): string {
  if (!item) {
    return "";
  }
  if ("content" in item) {
    return item.content;
  }
  return `${item.predicate}: ${item.object}`;
}

function tierLabel(hit: RecallHit): string {
  const tier = hit.tier?.trim();
  if (tier) {
    return `Tier ${tier}`;
  }
  return recallResultKindLabel(recallResultKind(hit));
}

function recallResultKind(hit: RecallHit): RecallResultKind {
  if (hit.fact) {
    return "fact";
  }
  if (hit.claim) {
    return "claim";
  }
  return "fragment";
}

function recallResultKindLabel(kind: RecallResultKind): string {
  if (kind === "fact") {
    return "Fact";
  }
  if (kind === "claim") {
    return "Claim";
  }
  return "Fragment";
}

function resultItem(hit: RecallHit): Fact | Claim | Fragment | undefined {
  return hit.fact ?? hit.claim ?? hit.fragment;
}

function recallResultStatus(item: Fact | Claim | Fragment | undefined): RecallResultStatus {
  const raw = item?.status?.toLowerCase() ?? "";
  if (raw.includes("disputed") || raw.includes("contradicted") || raw.includes("rejected") || raw.includes("invalid")) {
    return "disputed";
  }
  if (raw.includes("deprecated") || raw.includes("retracted") || raw.includes("stale") || raw.includes("superseded")) {
    return "deprecated";
  }
  if (raw.includes("candidate") || raw.includes("pending") || raw.includes("provisional") || raw.includes("unverified") || raw.includes("draft")) {
    return "provisional";
  }
  return "verified";
}

function recallResultStatusLabel(status: RecallResultStatus): string {
  if (status === "verified") {
    return "Verified";
  }
  if (status === "provisional") {
    return "Provisional";
  }
  if (status === "disputed") {
    return "Disputed";
  }
  return "Deprecated";
}

function statusPillClass(status: RecallResultStatus): string {
  if (status === "verified") {
    return "status-pill success";
  }
  if (status === "provisional") {
    return "status-pill warning";
  }
  if (status === "disputed") {
    return "status-pill danger";
  }
  return "status-pill neutral";
}

function sourceMatches(item: Fact | Claim | Fragment | undefined, sourceFilter: string): boolean {
  return sourceFilter === "all" || sourceLabel(item) === sourceFilter;
}

function dateMatches(item: Fact | Claim | Fragment | undefined, dateMode: "all" | "custom", startDate: string, endDate: string): boolean {
  if (dateMode === "all" || (!startDate && !endDate)) {
    return true;
  }
  const raw = itemRawDate(item);
  if (!raw) {
    return false;
  }
  const itemTime = new Date(raw).getTime();
  if (Number.isNaN(itemTime)) {
    return false;
  }
  if (startDate && itemTime < startOfDay(startDate)) {
    return false;
  }
  if (endDate && itemTime > endOfDay(endDate)) {
    return false;
  }
  return true;
}

function sortRecallResults(items: IndexedRecallResult[], sortMode: RecallSortMode): IndexedRecallResult[] {
  if (sortMode === "relevance") {
    return items;
  }
  return [...items].sort((left, right) => {
    return itemTimestamp(right) - itemTimestamp(left);
  });
}

function itemTimestamp(result: IndexedRecallResult): number {
  const value = new Date(itemRawDate(resultItem(result.hit))).getTime();
  return Number.isNaN(value) ? 0 : value;
}

function itemRawDate(item: Fact | Claim | Fragment | undefined): string {
  if (!item) {
    return "";
  }
  return "content" in item ? item.created_at : item.recorded_at;
}

function startOfDay(value: string): number {
  return new Date(`${value}T00:00:00`).getTime();
}

function endOfDay(value: string): number {
  return new Date(`${value}T23:59:59.999`).getTime();
}

function resultIcon(kind: RecallResultKind) {
  if (kind === "fact") {
    return <ShieldCheck size={15} aria-hidden="true" />;
  }
  if (kind === "claim") {
    return <GitBranch size={15} aria-hidden="true" />;
  }
  return <FileText size={15} aria-hidden="true" />;
}

function recallKey(hit: RecallHit, index: number): string {
  return hit.fact?.fact_id ?? hit.claim?.claim_id ?? hit.fragment?.fragment_id ?? String(index);
}

function sourceLabel(item: Fact | Claim | Fragment | undefined): string {
  if (!item) {
    return "unknown";
  }
  if ("content" in item) {
    return item.source || item.source_type || "fragment";
  }
  return "knowledge graph";
}

function itemDate(item: Fact | Claim | Fragment | undefined): string {
  if (!item) {
    return "";
  }
  const raw = "content" in item ? item.created_at : item.recorded_at;
  return new Date(raw).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function confidencePercent(hit: RecallHit): string {
  const score = hit.score ?? hit.final_score ?? 0;
  return `${Math.max(0, Math.min(1, score)) * 100}%`;
}

function scoreLabel(value: number | undefined): string {
  if (value === undefined || Number.isNaN(value)) {
    return "";
  }
  return value.toFixed(value >= 1 ? 0 : 3);
}

function rankLabel(value: number | undefined): string {
  return value === undefined ? "n/a" : String(value);
}

function readError(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed.";
}

function shortId(id: string): string {
  return id.length <= 8 ? id : id.slice(0, 8);
}
