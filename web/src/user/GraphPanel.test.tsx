import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { GraphPanel } from "./GraphPanel";
import { GraphSnapshot, UserApi } from "./api";

vi.mock("react-force-graph-2d", async () => {
  const React = await import("react");
  return {
    default: React.forwardRef(function MockForceGraph(props: {
      graphData?: { nodes?: Array<{ key: string; title: string }> };
    }, ref) {
      React.useImperativeHandle(ref, () => ({
        d3Force: () => ({ distance: () => undefined }),
        d3ReheatSimulation: () => undefined,
        zoomToFit: () => undefined,
      }));
      return (
        <div data-testid="force-graph">
          {(props.graphData?.nodes ?? []).map((node) => (
            <span key={node.key}>{node.title}</span>
          ))}
        </div>
      );
    }),
  };
});

describe("GraphPanel", () => {
  it("offers semantic graph types as local anchors", async () => {
    render(<GraphPanel api={graphApi().api} />);

    const controls = await screen.findByLabelText("Graph controls");
    await userEvent.click(within(controls).getByRole("button", { name: "Local" }));

    const anchorType = within(controls).getByRole("combobox", { name: "Anchor type" });
    expect(within(anchorType).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Entity",
      "Value",
    ]);
  });

  it("requests depth five and relationship limits above the former cap", async () => {
    const harness = graphApi();
    render(<GraphPanel api={harness.api} />);

    const controls = await screen.findByLabelText("Graph controls");
    expect(within(controls).getByLabelText("Relationship limit")).toHaveValue(80);
    expect(within(controls).getByLabelText("Depth")).toHaveAttribute("max", "5");
    await userEvent.click(within(controls).getByRole("button", { name: "Local" }));
    await userEvent.type(within(controls).getByLabelText("Anchor ID"), "entity-1");
    fireEvent.change(within(controls).getByLabelText("Depth"), { target: { value: "5" } });
    fireEvent.change(within(controls).getByLabelText("Relationship limit"), { target: { value: "181" } });
    await userEvent.click(within(controls).getByRole("button", { name: "Refresh" }));

    await waitFor(() => expect(harness.graph).toHaveBeenLastCalledWith(expect.objectContaining({
      scope: "local",
      anchorId: "entity-1",
      depth: 5,
      limit: 181,
    })));
  });

  it("rejects invalid relationship limits before requesting", async () => {
    const harness = graphApi();
    render(<GraphPanel api={harness.api} />);

    const controls = await screen.findByLabelText("Graph controls");
    await waitFor(() => expect(harness.graph).toHaveBeenCalledTimes(1));
    fireEvent.change(within(controls).getByLabelText("Relationship limit"), { target: { value: "1.5" } });
    await userEvent.click(within(controls).getByRole("button", { name: "Refresh" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("positive integer");
    expect(harness.graph).toHaveBeenCalledTimes(1);
  });

  it("replaces stale graph nodes on refresh", async () => {
    const before = snapshotWithNode("entity:wrong", "Wrong Project");
    const after = snapshotWithNode("entity:correct", "Dense-Mem");
    const harness = graphApi([before, after]);
    render(<GraphPanel api={harness.api} />);

    expect(await screen.findByText("Wrong Project")).toBeInTheDocument();
    await userEvent.click(within(screen.getByLabelText("Graph controls")).getByRole("button", { name: "Refresh" }));

    expect(await screen.findByText("Dense-Mem")).toBeInTheDocument();
    expect(screen.queryByText("Wrong Project")).not.toBeInTheDocument();
  });
});

function graphApi(snapshots?: GraphSnapshot[]): { api: UserApi; graph: ReturnType<typeof vi.fn> } {
  const snapshot: GraphSnapshot = {
    scope: "overview",
    depth: 2,
    limit: 80,
    truncated: false,
    nodes: [],
    edges: [],
  };
  const graph = vi.fn();
  for (const item of snapshots ?? [snapshot]) {
    graph.mockResolvedValueOnce(item);
  }
  graph.mockResolvedValue(snapshots?.at(-1) ?? snapshot);
  return { api: {
    graph,
    nodeDetail: vi.fn(),
  } as unknown as UserApi, graph };
}

function snapshotWithNode(key: string, title: string): GraphSnapshot {
  const [, id] = key.split(":");
  return {
    scope: "overview",
    depth: 2,
    limit: 80,
    truncated: false,
    nodes: [{ key, id, type: "entity", title }],
    edges: [],
  };
}
