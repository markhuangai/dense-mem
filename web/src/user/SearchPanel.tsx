import { FormEvent, useMemo, useState } from "react";
import { Search } from "lucide-react";
import { SectionHeading } from "../ui/components";
import { Claim, Community, Fact, Fragment, RecallHit, UserApi } from "./api";

type RecallResultKind = "fact" | "claim" | "fragment";

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
    indexedHits.filter((item) => enabledTypes[item.kind])
  ), [indexedHits, enabledTypes]);
  const selectedResult = useMemo(() => (
    filteredHits.find((item) => item.key === selectedKey) ?? filteredHits[0] ?? null
  ), [filteredHits, selectedKey]);
  const typeCounts = useMemo(() => indexedHits.reduce<Record<RecallResultKind, number>>((counts, item) => ({
    ...counts,
    [item.kind]: counts[item.kind] + 1,
  }), { fact: 0, claim: 0, fragment: 0 }), [indexedHits]);

  function toggleType(kind: RecallResultKind) {
    setEnabledTypes((current) => ({
      ...current,
      [kind]: !current[kind],
    }));
  }

  return (
    <section className="knowledge-explorer" aria-label="Knowledge explorer">
      <aside className="knowledge-filter-panel" aria-label="Knowledge filters">
        <SectionHeading
          title="Filters"
          actions={(
            <button
              className="text-button"
              type="button"
              onClick={() => setEnabledTypes({ fact: true, claim: true, fragment: true })}
            >
              Reset
            </button>
          )}
        />
        <form className="search-form stacked" onSubmit={submit}>
          <label htmlFor="recall-query">Keyword</label>
          <div className="search-input-wrap">
            <Search size={16} aria-hidden="true" />
            <input
              id="recall-query"
              placeholder="Search knowledge..."
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <button className="primary-button" type="submit" disabled={loading}>
            <Search size={16} aria-hidden="true" />
            Search
          </button>
        </form>
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
        {entities.length > 0 && (
          <div className="filter-stack" aria-label="Related entities">
            <strong>Entities</strong>
            <div className="entity-strip">
              {entities.map((entity) => <span className="status-pill neutral" key={entity}>{entity}</span>)}
            </div>
          </div>
        )}
      </aside>

      <section className="knowledge-results-panel" aria-label="Recall results">
        <div className="knowledge-results-toolbar">
          <div>
            <h2>Recall</h2>
            <span>{filteredHits.length} results</span>
          </div>
        </div>
        {error && <div className="banner error" role="alert">{error}</div>}
        {loading && <div className="table-placeholder">Loading</div>}
        {!loading && (
          <RecallResults
            items={filteredHits}
            selectedKey={selectedResult?.key ?? ""}
            onSelect={setSelectedKey}
          />
        )}
      </section>

      <aside className="knowledge-inspector" aria-label="Inspector">
        <SectionHeading
          title="Inspector"
          meta={selectedResult ? recallResultKindLabel(selectedResult.kind) : undefined}
        />
        <KnowledgeInspector result={selectedResult} />
      </aside>
    </section>
  );
}

function RecallResults({
  items,
  selectedKey,
  onSelect,
}: {
  items: IndexedRecallResult[];
  selectedKey: string;
  onSelect: (key: string) => void;
}) {
  if (items.length === 0) {
    return <div className="table-placeholder">No recall results</div>;
  }
  return (
    <div className="knowledge-list compact-list" role="listbox" aria-label="Recall result list">
      {items.map((result) => {
        const item = result.hit.fact ?? result.hit.claim ?? result.hit.fragment;
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
              <span className="status-pill neutral">{recallResultKindLabel(result.kind)}</span>
              <small>{tierLabel(result.hit)} | {scoreLabel(result.hit.score ?? result.hit.final_score)}</small>
            </div>
            <h3>{itemTitle(item)}</h3>
            <p>{itemBody(item)}</p>
          </article>
        );
      })}
    </div>
  );
}

function KnowledgeInspector({ result }: { result: IndexedRecallResult | null }) {
  if (!result) {
    return <div className="table-placeholder compact">Select a result</div>;
  }

  const item = result.hit.fact ?? result.hit.claim ?? result.hit.fragment;
  return (
    <article className="inspector-card">
      <div className="knowledge-item-head">
        <span className="status-pill neutral">{recallResultKindLabel(result.kind)}</span>
        <small>{scoreLabel(result.hit.score ?? result.hit.final_score)}</small>
      </div>
      <h3>{itemTitle(item)}</h3>
      <p>{itemBody(item)}</p>
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

function recallKey(hit: RecallHit, index: number): string {
  return hit.fact?.fact_id ?? hit.claim?.claim_id ?? hit.fragment?.fragment_id ?? String(index);
}

function scoreLabel(value: number | undefined): string {
  if (value === undefined || Number.isNaN(value)) {
    return "";
  }
  return value.toFixed(value >= 1 ? 0 : 3);
}

function readError(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed.";
}

function shortId(id: string): string {
  return id.length <= 8 ? id : id.slice(0, 8);
}
