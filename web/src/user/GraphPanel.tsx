import { Label } from "../ui/primitives/label";
import { Input } from "../ui/primitives/input";
import { Button } from "../ui/primitives/button";
import { NativeSelect, NativeSelectOption } from "../ui/primitives/native-select";
import { Checkbox } from "../ui/primitives/checkbox";
import { Switch } from "../ui/primitives/switch";
import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import ForceGraph2D, { ForceGraphMethods, LinkObject, NodeObject } from "react-force-graph-2d";
import {
  CircleDot,
  Focus,
  Network,
  RefreshCw,
  Search,
  SlidersHorizontal,
} from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "../ui/primitives/popover";
import { ResponsiveInspector, useCompactWorkspace } from "../ui/inspector";
import { LoadingState, SectionHeading } from "../ui/components";
import { GraphEdge, GraphNode, GraphNodeType, GraphQuery, GraphSnapshot, UserApi } from "./api";

type TypeFilter = Record<GraphNodeType, boolean>;
type GraphAnchor = {
  type: GraphNodeType;
  id: string;
};

type ForceNode = NodeObject<GraphNode> & GraphNode;
type ForceLink = LinkObject<GraphNode, GraphEdge> & GraphEdge;

const graphNodeTypes: GraphNodeType[] = ["entity", "value"];
const defaultGraphLimit = 80;

const defaultTypes: TypeFilter = {
  entity: true,
  value: true,
};

export function GraphPanel({ api }: { api: UserApi }) {
  const { ref, compact } = useCompactWorkspace();
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const requestSequence = useRef(0);
  const [snapshot, setSnapshot] = useState<GraphSnapshot | null>(null);
  const [selectedKey, setSelectedKey] = useState("");
  const [searchText, setSearchText] = useState("");
  const [scope, setScope] = useState<"overview" | "local">("overview");
  const [anchorType, setAnchorType] = useState<GraphNodeType>("entity");
  const [anchorId, setAnchorId] = useState("");
  const [types, setTypes] = useState<TypeFilter>(defaultTypes);
  const [depth, setDepth] = useState(2);
  const [limitInput, setLimitInput] = useState(String(defaultGraphLimit));
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

  async function loadGraph(query: GraphQuery) {
    if (query.types?.length === 0) {
      setError("Select at least one type.");
      return;
    }
    if (query.scope === "local" && (!query.anchorType || !query.anchorId)) {
      setError("Anchor ID is required for local graph.");
      return;
    }
    const request = ++requestSequence.current;
    setLoading(true);
    setError("");
    try {
      const next = await api.graph(query);
      if (request !== requestSequence.current) return;
      setSnapshot(next);
      setSelectedDetail(null);
      setDetailKey("");
      setDetailError("");
      setDetailLoading(false);
      setGraphRevision((current) => current + 1);
      setSelectedKey((current) => next.nodes.some((node) => node.key === current) ? current : "");
    } catch (err) {
      if (request === requestSequence.current) setError(readError(err));
    } finally {
      if (request === requestSequence.current) setLoading(false);
    }
  }

  useEffect(() => {
    void loadGraph(buildQuery({ searchText: "", scope: "overview", anchorType, anchorId: "", types, depth: 2, limit: defaultGraphLimit }));
    return () => { requestSequence.current += 1; };
  }, [api]);

  const selectedNode = selectedKey ? snapshot?.nodes.find((node) => node.key === selectedKey) ?? null : null;
  const hasSelectedTypes = graphNodeTypes.some((type) => types[type]);
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
    const limit = parseGraphLimit(limitInput);
    if (limit === null) {
      setError("Relationship limit must be a positive integer.");
      return;
    }
    void loadGraph(buildQuery({ searchText, scope, anchorType, anchorId, types, depth, limit }));
  }

  function toggleType(type: GraphNodeType) {
    setTypes((current) => ({ ...current, [type]: !current[type] }));
  }

  return (
    <section ref={ref} className={inspectorOpen && !compact ? "graph-panel has-inspector" : "graph-panel"} aria-label="Knowledge graph">


      <section className="graph-canvas-panel" aria-label="Graph canvas">
        <div className="graph-toolbar">
          <div><h2>Knowledge graph</h2><span className="muted">{snapshot ? `${snapshot.nodes.length} nodes · ${snapshot.edges.length} edges` : "Explore connected knowledge"}</span></div>
          <div className="toolbar-actions">
            {snapshot && snapshot.nodes.length > 0 && <NativeSelect aria-label="Inspect graph node" value={selectedKey} onChange={(event) => { setSelectedKey(event.target.value); setInspectorOpen(Boolean(event.target.value)); }}><NativeSelectOption value="">Inspect a node…</NativeSelectOption>{snapshot.nodes.map((node) => <NativeSelectOption key={node.key} value={node.key}>{node.title} · {nodeTypeLabel(node.type)}</NativeSelectOption>)}</NativeSelect>}
            <Popover><PopoverTrigger asChild><Button variant="outline"><SlidersHorizontal size={16} aria-hidden="true" />Graph controls</Button></PopoverTrigger>
      <PopoverContent className="explorer-filter-popover graph-controls" align="start" aria-label="Graph controls">
        <SectionHeading
          title="Graph"
          meta={snapshot ? `${snapshot.nodes.length} nodes / ${snapshot.edges.length} edges` : undefined}
        />
        <form className="graph-control-form" noValidate onSubmit={submit}>
          <Label htmlFor="graph-query">Search</Label>
          <div className="search-input-wrap">
            <Search size={16} aria-hidden="true" />
            <Input
              id="graph-query"
              value={searchText}
              onChange={(event) => setSearchText(event.target.value)}
              placeholder="Filter nodes..."
            />
          </div>

          <div className="segmented-control" aria-label="Graph scope">
            <Button variant="outline" className={scope === "overview" ? "active" : ""} type="button" onClick={() => setScope("overview")}>
              <Network size={15} aria-hidden="true" />
              Overview
            </Button>
            <Button variant="outline" className={scope === "local" ? "active" : ""} type="button" onClick={() => setScope("local")}>
              <Focus size={15} aria-hidden="true" />
              Local
            </Button>
          </div>

          {scope === "local" && (
            <div className="graph-anchor-grid">
              <Label htmlFor="graph-anchor-type">Anchor type</Label>
              <NativeSelect id="graph-anchor-type" value={anchorType} onChange={(event) => setAnchorType(event.target.value as GraphNodeType)}>
                {graphNodeTypes.map((type) => (
                  <NativeSelectOption key={type} value={type}>{nodeTypeLabel(type)}</NativeSelectOption>
                ))}
              </NativeSelect>
              <Label htmlFor="graph-anchor-id">Anchor ID</Label>
              <Input id="graph-anchor-id" value={anchorId} onChange={(event) => setAnchorId(event.target.value)} />
            </div>
          )}

          <fieldset className="graph-type-filter">
            <legend>Types</legend>
            {graphNodeTypes.map((type) => (
              <Label className="filter-row" key={type}>
                <Checkbox checked={types[type]} onCheckedChange={() => toggleType(type)} />
                <span>{nodeTypeLabel(type)}</span>
              </Label>
            ))}
          </fieldset>

          <div className="graph-slider-row">
            <Label htmlFor="graph-depth">Depth</Label>
            <Input id="graph-depth" type="range" min={1} max={5} step={1} value={depth} disabled={scope !== "local"} onChange={(event) => setDepth(Number(event.target.value))} />
            <span>{depth}</span>
          </div>
          <div className="graph-limit-row">
            <Label htmlFor="graph-relationship-limit">Relationship limit</Label>
            <Input
              id="graph-relationship-limit"
              type="number"
              min={1}
              step={1}
              inputMode="numeric"
              value={limitInput}
              onChange={(event) => setLimitInput(event.target.value)}
            />
          </div>
          <div className="graph-slider-row">
            <Label htmlFor="graph-node-size">Node size</Label>
            <Input id="graph-node-size" type="range" min={3} max={9} step={1} value={nodeSize} onChange={(event) => setNodeSize(Number(event.target.value))} />
            <span>{nodeSize}</span>
          </div>
          <div className="graph-slider-row">
            <Label htmlFor="graph-link-distance">Link distance</Label>
            <Input id="graph-link-distance" type="range" min={44} max={180} step={4} value={linkDistance} onChange={(event) => setLinkDistance(Number(event.target.value))} />
            <span>{linkDistance}</span>
          </div>

          <Label className="toggle-row compact-toggle">
            <span>Arrows</span>
            <Switch checked={showArrows} onCheckedChange={(event) => setShowArrows((event === true))} />
          </Label>
          <Label className="toggle-row compact-toggle">
            <span>Labels</span>
            <Switch checked={showLabels} onCheckedChange={(event) => setShowLabels((event === true))} />
          </Label>
          <div className="button-row">
            <Button variant="default" className="primary-button compact" type="submit" disabled={loading || !hasSelectedTypes}>
              <RefreshCw size={16} aria-hidden="true" />
              Refresh
            </Button>
          </div>
        </form>
      </PopoverContent>
            </Popover>
            {!inspectorOpen && <Button className="explorer-details-trigger" variant="outline" onClick={() => setInspectorOpen(true)}>Open details</Button>}
          </div>
        </div>
        {error && <div className="banner error" role="alert">{error}</div>}
        {loading && !snapshot && <LoadingState label="Loading graph" />}
        {snapshot && (
          <GraphCanvas
            snapshot={snapshot}
            selectedKey={selectedKey}
            onSelect={(key) => { setSelectedKey(key); setInspectorOpen(true); }}
            nodeSize={nodeSize}
            linkDistance={linkDistance}
            showArrows={showArrows}
            showLabels={showLabels}
          />
        )}
        {!loading && snapshot && snapshot.nodes.length === 0 && <div className="table-placeholder">No graph nodes</div>}
      </section>

      <ResponsiveInspector compact={compact} open={inspectorOpen} onOpenChange={setInspectorOpen} title="Graph inspector">
        <GraphStats snapshot={snapshot} />
        <NodeInspector summary={selectedNode} detail={activeDetail} loading={activeDetailLoading} error={activeDetailError} />
      </ResponsiveInspector>
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
      types: graphNodeTypes,
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
  nodeSize,
  linkDistance,
  showArrows,
  showLabels,
  compact = false,
}: {
  snapshot: GraphSnapshot;
  selectedKey: string;
  onSelect: (key: string) => void;
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
  limit,
}: {
  searchText: string;
  scope: "overview" | "local";
  anchorType: GraphNodeType;
  anchorId: string;
  types: TypeFilter;
  depth: number;
  limit: number;
}): GraphQuery {
  const enabledTypes = graphNodeTypes.filter((type) => types[type]);
  return {
    scope,
    q: searchText.trim() || undefined,
    types: enabledTypes,
    anchorType: scope === "local" ? anchorType : undefined,
    anchorId: scope === "local" ? anchorId.trim() : undefined,
    depth,
    limit,
  };
}

function parseGraphLimit(value: string): number | null {
  const parsed = Number(value.trim());
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null;
}

function drawNode(
  node: ForceNode,
  canvas: CanvasRenderingContext2D,
  globalScale: number,
  opts: { selected: boolean; nodeSize: number; showLabels: boolean; compact: boolean; theme: CanvasTheme },
) {
  const radius = nodeValue(node, opts.nodeSize);
  const color = nodeColor(node);
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

function nodeValue(node: Pick<GraphNode, "type">, nodeSize: number) {
	const typeBoost = node.type === "entity" ? 1.4 : node.type === "value" ? 0.8 : 1;
  return nodeSize + typeBoost;
}

function nodeColor(node: GraphNode): string {
	switch (node.type) {
		case "entity":
      return "#0e7490";
    case "value":
      return "#9333ea";
    default:
      return "#64748b";
  }
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
  if (type === "entity") {
    return "Entity";
  }
  if (type === "value") {
    return "Value";
  }
  return "Node";
}

function nodeIcon(type: string) {
	if (type === "entity") {
		return <CircleDot size={15} aria-hidden="true" />;
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
