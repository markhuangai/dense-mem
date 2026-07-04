import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import ForceGraph2D, { ForceGraphMethods, LinkObject, NodeObject } from "react-force-graph-2d";
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
type ColorMode = "type" | "community";
type GraphAnchor = {
  type: GraphNodeType;
  id: string;
};

type ForceNode = NodeObject<GraphNode> & GraphNode;
type ForceLink = LinkObject<GraphNode, GraphEdge> & GraphEdge;

const defaultTypes: TypeFilter = {
  fact: true,
  claim: true,
  fragment: true,
  dream: false,
};

export function GraphPanel({ api }: { api: UserApi }) {
  const [snapshot, setSnapshot] = useState<GraphSnapshot | null>(null);
  const [selectedKey, setSelectedKey] = useState("");
  const [searchText, setSearchText] = useState("");
  const [scope, setScope] = useState<"overview" | "local">("overview");
  const [anchorType, setAnchorType] = useState<GraphNodeType>("fact");
  const [anchorId, setAnchorId] = useState("");
  const [types, setTypes] = useState<TypeFilter>(defaultTypes);
  const [depth, setDepth] = useState(2);
  const [includeSuperseded, setIncludeSuperseded] = useState(false);
  const [nodeSize, setNodeSize] = useState(5);
  const [linkDistance, setLinkDistance] = useState(92);
  const [showArrows, setShowArrows] = useState(true);
  const [colorMode, setColorMode] = useState<ColorMode>("community");
  const [showLabels, setShowLabels] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

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
      setSelectedKey((current) => next.nodes.some((node) => node.key === current) ? current : next.anchor?.key ?? next.nodes[0]?.key ?? "");
    } catch (err) {
      setError(readError(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void loadGraph(buildQuery({ searchText: "", scope: "overview", anchorType, anchorId: "", types, depth: 2, includeSuperseded }));
  }, [api]);

  const selectedNode = snapshot?.nodes.find((node) => node.key === selectedKey) ?? snapshot?.nodes[0] ?? null;
  const hasSelectedTypes = Object.values(types).some(Boolean);

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
                <option value="fact">Fact</option>
                <option value="claim">Claim</option>
                <option value="fragment">Fragment</option>
                <option value="dream">Dream</option>
              </select>
              <label htmlFor="graph-anchor-id">Anchor ID</label>
              <input id="graph-anchor-id" value={anchorId} onChange={(event) => setAnchorId(event.target.value)} />
            </div>
          )}

          <fieldset className="graph-type-filter">
            <legend>Types</legend>
            {(["fact", "claim", "fragment", "dream"] as GraphNodeType[]).map((type) => (
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
          <label className="toggle-row compact-toggle">
            <span>Superseded</span>
            <input type="checkbox" checked={includeSuperseded} onChange={(event) => setIncludeSuperseded(event.target.checked)} />
          </label>
          <label className="toggle-row compact-toggle">
            <span>Communities</span>
            <input type="checkbox" checked={colorMode === "community"} onChange={(event) => setColorMode(event.target.checked ? "community" : "type")} />
          </label>

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
        {snapshot && (
          <GraphCanvas
            snapshot={snapshot}
            selectedKey={selectedKey}
            onSelect={setSelectedKey}
            colorMode={colorMode}
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
        <NodeInspector node={selectedNode} />
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
  return (
    <div className="graph-preview">
      <GraphCanvas
        snapshot={snapshot}
        selectedKey={selectedKey}
        onSelect={setSelectedKey}
        colorMode="type"
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
  onSelect,
  colorMode,
  nodeSize,
  linkDistance,
  showArrows,
  showLabels,
  compact = false,
}: {
  snapshot: GraphSnapshot;
  selectedKey: string;
  onSelect: (key: string) => void;
  colorMode: ColorMode;
  nodeSize: number;
  linkDistance: number;
  showArrows: boolean;
  showLabels: boolean;
  compact?: boolean;
}) {
  const graphRef = useRef<ForceGraphMethods<ForceNode, ForceLink> | undefined>(undefined);
  const { ref, width, height } = useElementSize<HTMLDivElement>(compact ? 220 : 620);
  const canvasTheme = useCanvasTheme(ref);
  const graphData = useMemo(() => ({
    nodes: snapshot.nodes.map((node) => ({ ...node, id: node.key, val: nodeValue(node, nodeSize) })),
    links: snapshot.edges.map((edge) => ({ ...edge, source: edge.source, target: edge.target })),
  }), [snapshot.nodes, snapshot.edges, nodeSize]);

  useEffect(() => {
    const linkForce = graphRef.current?.d3Force("link") as { distance?: (value: number) => unknown } | undefined;
    linkForce?.distance?.(linkDistance);
    graphRef.current?.d3ReheatSimulation();
  }, [linkDistance, graphData.links.length]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      graphRef.current?.zoomToFit(450, compact ? 28 : 54);
    }, 120);
    return () => window.clearTimeout(timer);
  }, [snapshot.nodes.length, snapshot.edges.length, compact]);

  return (
    <div className={compact ? "graph-canvas compact" : "graph-canvas"} ref={ref}>
      <ForceGraph2D<ForceNode, ForceLink>
        ref={graphRef}
        graphData={graphData}
        nodeId="key"
        width={width}
        height={height}
        backgroundColor="transparent"
        nodeVal={(node) => node.val ?? nodeValue(node, nodeSize)}
        nodeLabel={(node) => `${nodeTypeLabel(node.type as GraphNodeType)}: ${node.title}`}
        nodeCanvasObject={(node, canvas, globalScale) => drawNode(node, canvas, globalScale, {
          colorMode,
          selected: node.key === selectedKey,
          nodeSize,
          showLabels,
          compact,
          theme: canvasTheme,
        })}
        nodePointerAreaPaint={(node, color, canvas) => paintNodeArea(node, color, canvas, nodeSize)}
        linkLabel={(link) => relationshipLabel(link.relationship)}
        linkColor={(link) => relationshipColor(link.relationship)}
        linkWidth={(link) => selectedKey && (link.source === selectedKey || link.target === selectedKey || nodeKey(link.source) === selectedKey || nodeKey(link.target) === selectedKey) ? 2.2 : 1.2}
        linkDirectionalArrowLength={showArrows ? 5 : 0}
        linkDirectionalArrowRelPos={0.78}
        linkDirectionalArrowColor={(link) => relationshipColor(link.relationship)}
        onNodeClick={(node) => onSelect(node.key)}
        cooldownTicks={compact ? 60 : 120}
        d3VelocityDecay={0.36}
        minZoom={0.2}
        maxZoom={5}
      />
    </div>
  );
}

function useElementSize<T extends HTMLElement>(fallbackHeight: number) {
  const ref = useRef<T | null>(null);
  const [size, setSize] = useState({ width: 640, height: fallbackHeight });

  useEffect(() => {
    const node = ref.current;
    if (!node) {
      return;
    }
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect) {
        return;
      }
      setSize({
        width: Math.max(280, Math.floor(rect.width)),
        height: Math.max(180, Math.floor(rect.height || fallbackHeight)),
      });
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [fallbackHeight]);

  return { ref, ...size };
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

function NodeInspector({ node }: { node: GraphNode | null }) {
  if (!node) {
    return <div className="table-placeholder compact">Select a node</div>;
  }
  return (
    <article className="graph-node-detail">
      <div className="result-kicker">
        {nodeIcon(node.type)}
        <span className="status-pill neutral">{nodeTypeLabel(node.type as GraphNodeType)}</span>
        {node.status && <span className="status-pill success">{node.status}</span>}
      </div>
      <h3>{node.title || node.id}</h3>
      {node.body && <p>{node.body}</p>}
      <dl className="evidence-list">
        <div>
          <dt>ID</dt>
          <dd>{node.id}</dd>
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

function drawNode(
  node: ForceNode,
  canvas: CanvasRenderingContext2D,
  globalScale: number,
  opts: { colorMode: ColorMode; selected: boolean; nodeSize: number; showLabels: boolean; compact: boolean; theme: CanvasTheme },
) {
  const radius = nodeValue(node, opts.nodeSize);
  const color = nodeColor(node, opts.colorMode);
  canvas.beginPath();
  canvas.arc(node.x ?? 0, node.y ?? 0, radius, 0, 2 * Math.PI, false);
  canvas.fillStyle = color;
  canvas.fill();
  canvas.lineWidth = opts.selected ? 3 / globalScale : 1.2 / globalScale;
  canvas.strokeStyle = opts.selected ? opts.theme.selectedStroke : opts.theme.stroke;
  canvas.stroke();

  if (!opts.compact && (opts.showLabels || globalScale > 1.4)) {
    const label = truncateLabel(node.title || node.id, 26);
    const fontSize = Math.max(8, 12 / globalScale);
    canvas.font = `${fontSize}px Inter, ui-sans-serif, system-ui`;
    canvas.textAlign = "center";
    canvas.textBaseline = "top";
    canvas.fillStyle = opts.theme.label;
    canvas.fillText(label, node.x ?? 0, (node.y ?? 0) + radius + 3);
  }
}

function paintNodeArea(node: ForceNode, color: string, canvas: CanvasRenderingContext2D, nodeSize: number) {
  canvas.fillStyle = color;
  canvas.beginPath();
  canvas.arc(node.x ?? 0, node.y ?? 0, nodeValue(node, nodeSize) + 6, 0, 2 * Math.PI, false);
  canvas.fill();
}

function nodeValue(node: Pick<GraphNode, "score" | "type">, nodeSize: number) {
  const scoreBoost = Math.max(0, Math.min(1, node.score ?? 0)) * 4;
  const typeBoost = node.type === "fact" ? 1.4 : node.type === "dream" ? 0.6 : 1;
  return nodeSize + scoreBoost + typeBoost;
}

function nodeColor(node: GraphNode, colorMode: ColorMode): string {
  if (colorMode === "community" && node.community_id) {
    return communityColor(node.community_id);
  }
  switch (node.type) {
    case "fact":
      return "#0f766e";
    case "claim":
      return "#2563eb";
    case "fragment":
      return "#ca8a04";
    case "dream":
      return "#c026d3";
    default:
      return "#64748b";
  }
}

function communityColor(communityID: string): string {
  const palette = ["#0f766e", "#2563eb", "#ca8a04", "#dc2626", "#7c3aed", "#0891b2", "#65a30d", "#db2777"];
  let hash = 0;
  for (let index = 0; index < communityID.length; index += 1) {
    hash = (hash * 31 + communityID.charCodeAt(index)) >>> 0;
  }
  return palette[hash % palette.length];
}

function relationshipColor(type: string): string {
  switch (type) {
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

function nodeKey(value: string | number | NodeObject<GraphNode> | undefined): string {
  if (!value) {
    return "";
  }
  if (typeof value === "string" || typeof value === "number") {
    return String(value);
  }
  return value.key ?? "";
}

function truncateLabel(value: string, maxLength: number): string {
  return value.length <= maxLength ? value : `${value.slice(0, maxLength - 3)}...`;
}

function readError(error: unknown): string {
  return error instanceof Error ? error.message : "Request failed.";
}
