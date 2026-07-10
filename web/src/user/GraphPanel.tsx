import { FormEvent, useEffect, useRef, useState } from "react";
import Graph from "graphology";
import FA2Layout from "graphology-layout-forceatlas2/worker";
import Sigma from "sigma";
import { EdgeArrowProgram, EdgeLineProgram, NodeCircleProgram } from "sigma/rendering";
import {
  ArrowRight,
  CircleDot,
  Focus,
  GitBranch,
  Network,
  RefreshCw,
  Search,
  SlidersHorizontal,
} from "lucide-react";
import { LoadingState, SectionHeading } from "../ui/components";
import { GraphEdge, GraphNode, GraphNodeType, GraphQuery, GraphSnapshot, UserApi } from "./api";

type TypeFilter = Record<GraphNodeType, boolean>;
type GraphAnchor = {
  type: GraphNodeType;
  id: string;
};

const defaultTypes: TypeFilter = {
	entity: true,
	value: true,
	fact: true,
	claim: true,
	fragment: true,
	dream: true,
	community: true,
};

export function GraphPanel({ api }: { api: UserApi }) {
  const [snapshot, setSnapshot] = useState<GraphSnapshot | null>(null);
	const [selectedKey, setSelectedKey] = useState("");
	const [selectedEdgeId, setSelectedEdgeId] = useState("");
  const [searchText, setSearchText] = useState("");
  const [scope, setScope] = useState<"overview" | "local">("overview");
  const [anchorType, setAnchorType] = useState<GraphNodeType>("fact");
  const [anchorId, setAnchorId] = useState("");
  const [types, setTypes] = useState<TypeFilter>(defaultTypes);
  const [depth, setDepth] = useState(2);
	const [includeSuperseded] = useState(true);
  const [nodeSize, setNodeSize] = useState(5);
  const [linkDistance, setLinkDistance] = useState(92);
  const [showArrows, setShowArrows] = useState(true);
  const [showLabels, setShowLabels] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [selectedDetail, setSelectedDetail] = useState<GraphNode | null>(null);
  const [detailKey, setDetailKey] = useState("");
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState("");
  const [graphRevision, setGraphRevision] = useState(0);

  async function loadGraph(query: GraphQuery = buildQuery({ searchText, scope, anchorType, anchorId, types, depth, includeSuperseded })) {
    if (query.types?.length === 0) {
      setError("Select at least one type.");
      return;
    }
    if (query.scope === "local" && (!query.anchorType || !query.anchorId)) {
      setError("Anchor ID is required for local graph.");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const next = await api.graph(query);
      setSnapshot(next);
		setSelectedDetail(null);
		setSelectedEdgeId("");
      setDetailKey("");
      setDetailError("");
      setDetailLoading(false);
      setGraphRevision((current) => current + 1);
      setSelectedKey((current) => next.nodes.some((node) => node.key === current) ? current : "");
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadGraph(buildQuery({ searchText: "", scope: "overview", anchorType, anchorId: "", types, depth: 2, includeSuperseded }));
  }, [api]);

	const selectedNode = selectedKey ? snapshot?.nodes.find((node) => node.key === selectedKey) ?? null : null;
	const selectedEdge = selectedEdgeId ? snapshot?.edges.find((edge) => edge.id === selectedEdgeId) ?? null : null;
  const hasSelectedTypes = Object.values(types).some(Boolean);
  const selectedNodeKey = selectedNode?.key ?? "";
  const activeDetail = selectedDetail?.key === selectedNodeKey ? selectedDetail : null;
  const activeDetailLoading = detailKey === selectedNodeKey ? detailLoading : false;
  const activeDetailError = detailKey === selectedNodeKey ? detailError : "";

  useEffect(() => {
    if (!selectedNode) {
      setSelectedDetail(null);
      setDetailKey("");
      setDetailError("");
      setDetailLoading(false);
      return;
    }
    let active = true;
    setSelectedDetail(null);
    setDetailKey(selectedNode.key);
    setDetailError("");
    setDetailLoading(true);
    api.nodeDetail(selectedNode.type, selectedNode.id)
      .then((detail) => {
        if (active) {
          setSelectedDetail(detail);
        }
      })
      .catch((err) => {
        if (active) {
          setDetailError(readError(err));
        }
      })
      .finally(() => {
        if (active) {
          setDetailLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, [api, selectedNode?.key, selectedNode?.type, selectedNode?.id, graphRevision]);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void loadGraph();
  }

  function toggleType(type: GraphNodeType) {
    setTypes((current) => ({ ...current, [type]: !current[type] }));
  }

  return (
    <section className="graph-panel" aria-label="Knowledge graph">
      <aside className="graph-controls" aria-label="Graph controls">
        <SectionHeading
          title="Graph"
          meta={snapshot ? `${snapshot.nodes.length} nodes / ${snapshot.edges.length} edges` : undefined}
        />
        <form className="graph-control-form" onSubmit={submit}>
          <label htmlFor="graph-query">Search</label>
          <div className="search-input-wrap">
            <Search size={16} aria-hidden="true" />
            <input
              id="graph-query"
              value={searchText}
              onChange={(event) => setSearchText(event.target.value)}
              placeholder="Filter nodes..."
            />
          </div>

          <div className="segmented-control" aria-label="Graph scope">
            <button className={scope === "overview" ? "active" : ""} type="button" onClick={() => setScope("overview")}>
              <Network size={15} aria-hidden="true" />
              Overview
            </button>
            <button className={scope === "local" ? "active" : ""} type="button" onClick={() => setScope("local")}>
              <Focus size={15} aria-hidden="true" />
              Local
            </button>
          </div>

          {scope === "local" && (
            <div className="graph-anchor-grid">
              <label htmlFor="graph-anchor-type">Anchor type</label>
              <select id="graph-anchor-type" value={anchorType} onChange={(event) => setAnchorType(event.target.value as GraphNodeType)}>
				<option value="entity">Entity</option>
				<option value="value">Value</option>
                <option value="fact">Fact</option>
                <option value="claim">Claim</option>
                <option value="fragment">Fragment</option>
                <option value="dream">Dream</option>
				<option value="community">Community</option>
              </select>
              <label htmlFor="graph-anchor-id">Anchor ID</label>
              <input id="graph-anchor-id" value={anchorId} onChange={(event) => setAnchorId(event.target.value)} />
            </div>
          )}

          <fieldset className="graph-type-filter">
            <legend>Types</legend>
			{(["entity", "value", "fact", "claim", "fragment", "dream", "community"] as GraphNodeType[]).map((type) => (
              <label className="filter-row" key={type}>
                <input type="checkbox" checked={types[type]} onChange={() => toggleType(type)} />
                <span>{nodeTypeLabel(type)}</span>
              </label>
            ))}
          </fieldset>

          <div className="graph-slider-row">
            <label htmlFor="graph-depth">Depth</label>
            <input id="graph-depth" type="range" min={1} max={2} step={1} value={depth} disabled={scope !== "local"} onChange={(event) => setDepth(Number(event.target.value))} />
            <span>{depth}</span>
          </div>
          <div className="graph-slider-row">
            <label htmlFor="graph-node-size">Node size</label>
            <input id="graph-node-size" type="range" min={3} max={9} step={1} value={nodeSize} onChange={(event) => setNodeSize(Number(event.target.value))} />
            <span>{nodeSize}</span>
          </div>
          <div className="graph-slider-row">
            <label htmlFor="graph-link-distance">Link distance</label>
            <input id="graph-link-distance" type="range" min={44} max={180} step={4} value={linkDistance} onChange={(event) => setLinkDistance(Number(event.target.value))} />
            <span>{linkDistance}</span>
          </div>

          <label className="toggle-row compact-toggle">
            <span>Arrows</span>
            <input type="checkbox" checked={showArrows} onChange={(event) => setShowArrows(event.target.checked)} />
          </label>
          <label className="toggle-row compact-toggle">
            <span>Labels</span>
            <input type="checkbox" checked={showLabels} onChange={(event) => setShowLabels(event.target.checked)} />
          </label>
		  <div className="graph-all-state-note">All lifecycle states are included. Embeddings and secret-like values are excluded.</div>

          <div className="button-row">
            <button className="primary-button compact" type="submit" disabled={loading || !hasSelectedTypes}>
              <RefreshCw size={16} aria-hidden="true" />
              Refresh
            </button>
          </div>
        </form>
      </aside>

      <section className="graph-canvas-panel" aria-label="Graph canvas">
        {error && <div className="banner error" role="alert">{error}</div>}
        {loading && !snapshot && <LoadingState label="Loading graph" />}
        {snapshot && snapshot.nodes.length > 0 && (
          <GraphCanvas
            snapshot={snapshot}
			selectedKey={selectedKey}
			selectedEdgeId={selectedEdgeId}
			onSelect={(key) => { setSelectedKey(key); setSelectedEdgeId(""); }}
			onSelectEdge={(id) => { setSelectedEdgeId(id); setSelectedKey(""); }}
            nodeSize={nodeSize}
            linkDistance={linkDistance}
            showArrows={showArrows}
            showLabels={showLabels}
          />
        )}
        {!loading && snapshot && snapshot.nodes.length === 0 && <div className="table-placeholder">No graph nodes</div>}
      </section>

	  <aside className="graph-inspector" aria-label="Graph inspector">
		<GraphStats snapshot={snapshot} />
		{selectedEdge ? <EdgeInspector edge={selectedEdge} snapshot={snapshot} /> : <NodeInspector summary={selectedNode} detail={activeDetail} loading={activeDetailLoading} error={activeDetailError} />}
      </aside>
    </section>
  );
}

export function ResultGraphPreview({ api, anchor }: { api: UserApi; anchor: GraphAnchor | null }) {
  const [snapshot, setSnapshot] = useState<GraphSnapshot | null>(null);
  const [selectedKey, setSelectedKey] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!anchor) {
      setSnapshot(null);
      setSelectedKey("");
      return;
    }
    let active = true;
    setLoading(true);
    setError("");
    api.graph({
      scope: "local",
      anchorType: anchor.type,
      anchorId: anchor.id,
      depth: 2,
      limit: 48,
      types: ["fact", "claim", "fragment", "dream"],
    })
      .then((next) => {
        if (active) {
          setSnapshot(next);
          setSelectedKey(next.anchor?.key ?? next.nodes[0]?.key ?? "");
        }
      })
      .catch((err) => {
        if (active) {
          setError(readError(err));
        }
      })
      .finally(() => {
        if (active) {
          setLoading(false);
        }
      });
    return () => {
      active = false;
    };
  }, [api, anchor?.type, anchor?.id]);

  if (!anchor) {
    return <div className="table-placeholder compact">No graph anchor</div>;
  }
  if (loading && !snapshot) {
    return <LoadingState label="Loading graph" compact />;
  }
  if (error) {
    return <div className="banner error" role="alert">{error}</div>;
  }
  if (!snapshot) {
    return <div className="table-placeholder compact">No graph data</div>;
  }
  if (snapshot.nodes.length === 0) {
    return <div className="table-placeholder compact">No graph nodes</div>;
  }
  return (
    <div className="graph-preview">
	  <GraphCanvas
        snapshot={snapshot}
        selectedKey={selectedKey}
		onSelect={setSelectedKey}
		selectedEdgeId=""
		onSelectEdge={() => undefined}
        nodeSize={4}
        linkDistance={72}
        showArrows
        showLabels={false}
        compact
      />
      <div className="graph-preview-meta">
        <span>{snapshot.nodes.length} nodes</span>
        <span>{snapshot.edges.length} edges</span>
      </div>
    </div>
  );
}

function GraphCanvas({
  snapshot,
  selectedKey,
  selectedEdgeId,
  onSelect,
  onSelectEdge,
  nodeSize,
  linkDistance,
  showArrows,
  showLabels,
  compact = false,
}: {
  snapshot: GraphSnapshot;
  selectedKey: string;
  selectedEdgeId: string;
  onSelect: (key: string) => void;
  onSelectEdge: (id: string) => void;
  nodeSize: number;
  linkDistance: number;
  showArrows: boolean;
  showLabels: boolean;
  compact?: boolean;
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const rendererRef = useRef<Sigma | null>(null);
  const selectNodeRef = useRef(onSelect);
  const selectEdgeRef = useRef(onSelectEdge);
  selectNodeRef.current = onSelect;
  selectEdgeRef.current = onSelectEdge;
  const canvasTheme = useCanvasTheme(containerRef);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    const graph = new Graph({ type: "directed", multi: true, allowSelfLoops: true });
    const goldenAngle = Math.PI * (3 - Math.sqrt(5));
    snapshot.nodes.forEach((node, index) => {
      const radius = Math.max(1, Math.sqrt(index + 1)) * linkDistance * 0.12;
      graph.addNode(node.key, {
        ...node,
        x: Math.cos(index * goldenAngle) * radius,
        y: Math.sin(index * goldenAngle) * radius,
        label: node.title || node.id,
        color: nodeColor(node),
        size: nodeValue(node, nodeSize),
        type: "circle",
      });
    });
    snapshot.edges.forEach((edge, index) => {
      if (!graph.hasNode(edge.source) || !graph.hasNode(edge.target)) {
        return;
      }
      graph.addDirectedEdgeWithKey(`${edge.id}:${index}`, edge.source, edge.target, {
        ...edge,
        snapshotEdgeId: edge.id,
        label: relationshipLabel(edge.relationship),
        color: relationshipColor(edge),
        size: edge.tier === "fact" ? 2.2 : edge.status === "quarantined" ? 1.8 : 1.15,
        type: showArrows ? "arrow" : "line",
      });
    });

    const renderer = new Sigma(graph, container, {
      allowInvalidContainer: true,
      defaultNodeType: "circle",
      defaultEdgeType: showArrows ? "arrow" : "line",
      nodeProgramClasses: { circle: NodeCircleProgram },
      edgeProgramClasses: { arrow: EdgeArrowProgram, line: EdgeLineProgram },
      renderLabels: showLabels,
      renderEdgeLabels: false,
      labelColor: { color: canvasTheme.label },
      edgeLabelColor: { color: canvasTheme.label },
      labelRenderedSizeThreshold: compact ? 100 : 9,
      labelDensity: compact ? 0.2 : 0.8,
      labelGridCellSize: compact ? 140 : 90,
      zIndex: true,
      minCameraRatio: 0.04,
      maxCameraRatio: 8,
    });
    rendererRef.current = renderer;
    renderer.on("clickNode", ({ node }) => selectNodeRef.current(node));
    renderer.on("clickEdge", ({ edge }) => {
      const id = graph.getEdgeAttribute(edge, "snapshotEdgeId") as string;
      if (id) {
        selectEdgeRef.current(id);
      }
    });
    renderer.getCamera().on("updated", (state) => {
      renderer.setSetting("renderEdgeLabels", showLabels && !compact && state.ratio < 0.7);
    });

    let layout: FA2Layout | null = null;
    let timer = 0;
    if (graph.order > 1 && typeof Worker !== "undefined") {
      layout = new FA2Layout(graph, {
        settings: {
          barnesHutOptimize: graph.order > 250,
          adjustSizes: true,
          edgeWeightInfluence: 0.7,
          gravity: compact ? 1.4 : 0.8,
          scalingRatio: Math.max(2, linkDistance / 26),
          slowDown: graph.order > 1000 ? 12 : 5,
        },
      });
      layout.start();
      timer = window.setTimeout(() => {
        layout?.stop();
        renderer.getCamera().animatedReset({ duration: 350 });
      }, compact ? 350 : Math.min(2200, 700 + graph.order));
    } else {
      renderer.getCamera().animatedReset({ duration: 0 });
    }

    return () => {
      if (timer) {
        window.clearTimeout(timer);
      }
      layout?.kill();
      renderer.kill();
      rendererRef.current = null;
    };
  }, [snapshot, nodeSize, linkDistance, showArrows, showLabels, compact, canvasTheme]);

  useEffect(() => {
    const renderer = rendererRef.current;
    if (!renderer) {
      return;
    }
    const selectedEdge = snapshot.edges.find((edge) => edge.id === selectedEdgeId);
    const selectedNodes = new Set(selectedEdge ? [selectedEdge.source, selectedEdge.target] : selectedKey ? [selectedKey] : []);
    renderer.setSetting("nodeReducer", (node, data) => {
      if (selectedNodes.size === 0) {
        return data;
      }
      if (selectedNodes.has(node)) {
        return { ...data, highlighted: true, size: Number(data.size ?? nodeSize) * 1.45, zIndex: 2 };
      }
      return { ...data, color: withAlpha(String(data.color ?? "#64748b"), 0.28), zIndex: 0 };
    });
    renderer.setSetting("edgeReducer", (edge, data) => {
      const id = renderer.getGraph().getEdgeAttribute(edge, "snapshotEdgeId") as string;
      if (!selectedEdgeId) {
        return data;
      }
      return id === selectedEdgeId
        ? { ...data, size: 3.4, zIndex: 2, forceLabel: true }
        : { ...data, color: withAlpha(String(data.color ?? "#64748b"), 0.2), zIndex: 0 };
    });
    renderer.refresh();
  }, [selectedKey, selectedEdgeId, snapshot.edges, nodeSize]);

  return (
	<div className={compact ? "graph-canvas compact" : "graph-canvas"} ref={containerRef} data-renderer="sigma-webgl" data-testid="sigma-graph">
	  <div className="sr-only" aria-label="Graph keyboard navigation">
		{snapshot.nodes.map((node) => (
		  <button key={node.key} type="button" onClick={() => onSelect(node.key)}>{node.title || node.id}</button>
		))}
		{snapshot.edges.map((edge) => {
		  const source = snapshot.nodes.find((node) => node.key === edge.source)?.title ?? edge.source;
		  const target = snapshot.nodes.find((node) => node.key === edge.target)?.title ?? edge.target;
		  return <button key={edge.id} type="button" onClick={() => onSelectEdge(edge.id)}>{source} {relationshipLabel(edge.relationship)} {target}</button>;
		})}
	  </div>
	</div>
  );
}

type CanvasTheme = {
  label: string;
  stroke: string;
  selectedStroke: string;
};

const defaultCanvasTheme: CanvasTheme = {
  label: "rgba(20, 31, 29, 0.92)",
  stroke: "rgba(255, 255, 255, 0.86)",
  selectedStroke: "#111827",
};

function useCanvasTheme<T extends HTMLElement>(ref: { current: T | null }): CanvasTheme {
  const [theme, setTheme] = useState(defaultCanvasTheme);

  useEffect(() => {
    const node = ref.current;
    if (!node) {
      return;
    }

    const readTheme = () => {
      const styles = getComputedStyle(node);
      const shell = node.closest(".app-shell, .auth-shell");
      const shellStyles = shell ? getComputedStyle(shell) : styles;
      const isDark = shellStyles.getPropertyValue("color-scheme").includes("dark");
      const next = {
        label: cssVar(shellStyles, "--text", defaultCanvasTheme.label),
        stroke: isDark ? "rgba(238, 247, 246, 0.82)" : defaultCanvasTheme.stroke,
        selectedStroke: cssVar(shellStyles, "--accent-strong", defaultCanvasTheme.selectedStroke),
      };
      setTheme((current) => sameCanvasTheme(current, next) ? current : next);
    };

    readTheme();
    const shell = node.closest(".app-shell, .auth-shell");
    if (!shell || typeof MutationObserver === "undefined") {
      return;
    }
    const observer = new MutationObserver(readTheme);
    observer.observe(shell, { attributes: true, attributeFilter: ["data-theme", "class", "style"] });
    return () => observer.disconnect();
  }, [ref]);

  return theme;
}

function cssVar(styles: CSSStyleDeclaration, name: string, fallback: string): string {
  return styles.getPropertyValue(name).trim() || fallback;
}

function sameCanvasTheme(left: CanvasTheme, right: CanvasTheme): boolean {
  return left.label === right.label && left.stroke === right.stroke && left.selectedStroke === right.selectedStroke;
}

function GraphStats({ snapshot }: { snapshot: GraphSnapshot | null }) {
  return (
    <section className="graph-stat-grid" aria-label="Graph totals">
      <div>
        <span>Nodes</span>
        <strong>{snapshot?.nodes.length ?? 0}</strong>
      </div>
      <div>
        <span>Edges</span>
        <strong>{snapshot?.edges.length ?? 0}</strong>
      </div>
      <div>
        <span>Depth</span>
        <strong>{snapshot?.depth ?? 0}</strong>
      </div>
    </section>
  );
}

function EdgeInspector({ edge, snapshot }: { edge: GraphEdge; snapshot: GraphSnapshot | null }) {
	const source = snapshot?.nodes.find((node) => node.key === edge.source);
	const target = snapshot?.nodes.find((node) => node.key === edge.target);
	return (
	  <article className="graph-node-detail graph-edge-detail">
		<div className="result-kicker">
		  <GitBranch size={15} aria-hidden="true" />
		  <span className="status-pill neutral">{relationshipLabel(edge.relationship)}</span>
		  {edge.tier && <span className="status-pill success">{edge.tier}</span>}
		  {edge.status && <span className="status-pill neutral">{edge.status}</span>}
		</div>
		<h3>{source?.title ?? edge.source} → {target?.title ?? edge.target}</h3>
		{edge.knowledge && <p>{edge.knowledge}</p>}
		<dl className="evidence-list">
		  <DetailRow label="Relationship" value={edge.relationship} />
		  <DetailRow label="Assertion" value={edge.assertion_id || "structural"} />
		  <DetailRow label="Predicate" value={edge.predicate || "n/a"} />
		  <DetailRow label="Policy" value={edge.policy_family || "n/a"} />
		  <DetailRow label="Polarity" value={edge.polarity || "n/a"} />
		  <DetailRow label="Support" value={`${edge.support_count ?? 0} spans / ${edge.source_group_count ?? 0} sources`} />
		  <DetailRow label="Valid" value={formatTimeRange(edge.valid_from, edge.valid_to)} />
		  <DetailRow label="Recorded" value={formatTimeRange(edge.recorded_at, edge.recorded_to)} />
		  <DetailRow label="Evidence IDs" value={edge.evidence_ids?.join(", ") || "none"} />
		</dl>
	  </article>
	);
}

function DetailRow({ label, value }: { label: string; value: string }) {
	return <div><dt>{label}</dt><dd>{value}</dd></div>;
}

function formatTimeRange(from?: string, to?: string): string {
	if (!from && !to) {
	  return "unbounded";
	}
	const format = (value?: string) => value ? new Date(value).toISOString() : "∞";
	return `${format(from)} → ${format(to)}`;
}

function NodeInspector({ summary, detail, loading, error }: { summary: GraphNode | null; detail: GraphNode | null; loading: boolean; error: string }) {
  if (!summary) {
    return <div className="table-placeholder compact">Select a node</div>;
  }
  const node = detail ?? summary;
  return (
    <article className="graph-node-detail">
      <div className="result-kicker">
        {nodeIcon(summary.type)}
        <span className="status-pill neutral">{nodeTypeLabel(summary.type as GraphNodeType)}</span>
        {node.status && <span className="status-pill success">{node.status}</span>}
      </div>
      <h3>{node.title || summary.title || summary.id}</h3>
      {loading && <LoadingState label="Loading details" compact />}
      {error && <div className="banner error" role="alert">{error}</div>}
      {node.body && <p>{node.body}</p>}
      <dl className="evidence-list">
        <div>
          <dt>ID</dt>
          <dd>{summary.id}</dd>
        </div>
        <div>
          <dt>Community</dt>
          <dd>{node.community_id || "none"}</dd>
        </div>
        <div>
          <dt>Score</dt>
          <dd>{typeof node.score === "number" ? node.score.toFixed(3) : "n/a"}</dd>
        </div>
		<div>
		  <dt>Recorded</dt>
		  <dd>{node.recorded_at ? new Date(node.recorded_at).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "n/a"}</dd>
		</div>
		{node.entity_type && <DetailRow label="Entity type" value={node.entity_type} />}
		{node.value_type && <DetailRow label="Value type" value={node.value_type} />}
		{node.resolution_status && <DetailRow label="Resolution" value={`${node.resolution_status} (${(node.resolution_conf ?? 0).toFixed(2)})`} />}
		{node.aliases?.length ? <DetailRow label="Aliases" value={node.aliases.join(", ")} /> : null}
	  </dl>
    </article>
  );
}

function buildQuery({
  searchText,
  scope,
  anchorType,
  anchorId,
  types,
  depth,
  includeSuperseded,
}: {
  searchText: string;
  scope: "overview" | "local";
  anchorType: GraphNodeType;
  anchorId: string;
  types: TypeFilter;
  depth: number;
  includeSuperseded: boolean;
}): GraphQuery {
  const enabledTypes = (Object.keys(types) as GraphNodeType[]).filter((type) => types[type]);
  return {
    scope,
    q: searchText.trim() || undefined,
    types: enabledTypes,
    anchorType: scope === "local" ? anchorType : undefined,
    anchorId: scope === "local" ? anchorId.trim() : undefined,
    depth,
    includeSuperseded,
  };
}

function nodeValue(node: Pick<GraphNode, "type">, nodeSize: number) {
	const typeBoost = node.type === "entity" ? 1.8 : node.type === "fact" ? 1.4 : node.type === "value" ? 0.2 : node.type === "dream" ? 0.6 : 1;
  return nodeSize + typeBoost;
}

function nodeColor(node: GraphNode): string {
  switch (node.type) {
	case "entity":
	  return "#7c3aed";
	case "value":
	  return "#64748b";
    case "fact":
      return "#0f766e";
    case "claim":
      return "#2563eb";
    case "fragment":
      return "#ca8a04";
	case "dream":
	  return "#c026d3";
	case "community":
	  return "#0891b2";
    default:
      return "#64748b";
  }
}

function relationshipColor(edge: GraphEdge): string {
	if (edge.status === "quarantined" || edge.status === "rejected") {
	  return "#dc2626";
	}
	if (edge.status === "needs_review") {
	  return "#ea580c";
	}
	if (edge.status === "superseded" || edge.status === "retracted") {
	  return "#94a3b8";
	}
	if (edge.tier === "fact") {
	  return "#0f766e";
	}
	if (edge.tier === "validated_claim") {
	  return "#2563eb";
	}
  switch (edge.relationship) {
    case "PROMOTES_TO":
      return "#0f766e";
    case "SUPPORTED_BY":
      return "#64748b";
    case "CONTRADICTS":
      return "#dc2626";
    case "SUPERSEDED_BY":
      return "#b45309";
    case "OVERLAYS":
      return "#ea580c";
    case "ALIGNS_WITH":
      return "#16a34a";
    case "DREAMS_FROM":
      return "#c026d3";
    default:
      return "#64748b";
  }
}

function withAlpha(color: string, alpha: number): string {
	const normalized = color.replace("#", "");
	if (!/^[0-9a-f]{6}$/i.test(normalized)) {
	  return color;
	}
	const red = Number.parseInt(normalized.slice(0, 2), 16);
	const green = Number.parseInt(normalized.slice(2, 4), 16);
	const blue = Number.parseInt(normalized.slice(4, 6), 16);
	return `rgba(${red}, ${green}, ${blue}, ${alpha})`;
}

function relationshipLabel(type: string): string {
  return type.toLowerCase().replaceAll("_", " ");
}

function nodeTypeLabel(type: GraphNodeType | string): string {
  if (type === "fact") {
    return "Fact";
  }
  if (type === "claim") {
    return "Claim";
  }
  if (type === "fragment") {
    return "Fragment";
  }
  if (type === "dream") {
    return "Dream";
  }
	if (type === "entity") {
	  return "Entity";
	}
	if (type === "value") {
	  return "Value";
	}
	if (type === "community") {
	  return "Community";
	}
  return "Node";
}

function nodeIcon(type: string) {
  if (type === "fact") {
    return <CircleDot size={15} aria-hidden="true" />;
  }
  if (type === "claim") {
    return <GitBranch size={15} aria-hidden="true" />;
  }
  if (type === "dream") {
    return <ArrowRight size={15} aria-hidden="true" />;
  }
  return <SlidersHorizontal size={15} aria-hidden="true" />;
}

function readError(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed.";
}
