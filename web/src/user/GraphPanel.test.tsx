import { render, screen, within } from "@testing-library/react";
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
  it("offers only active legacy graph types as local anchors", async () => {
    render(<GraphPanel api={graphApi()} />);

    const controls = await screen.findByLabelText("Graph controls");
    await userEvent.click(within(controls).getByRole("button", { name: "Local" }));

    const anchorType = within(controls).getByRole("combobox", { name: "Anchor type" });
    expect(within(anchorType).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Fact",
      "Claim",
      "Fragment",
      "Dream",
    ]);
  });
});

function graphApi(): UserApi {
  const snapshot: GraphSnapshot = {
    scope: "overview",
    depth: 2,
    limit: 80,
    truncated: false,
    nodes: [],
    edges: [],
  };
  return {
    graph: vi.fn().mockResolvedValue(snapshot),
    nodeDetail: vi.fn(),
  } as unknown as UserApi;
}
