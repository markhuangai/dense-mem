import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

vi.mock("sigma", () => {
  class TestSigma {
    private readonly graph: { getEdgeAttribute: (edge: string, attribute: string) => unknown };

    constructor(graph: { getEdgeAttribute: (edge: string, attribute: string) => unknown }) {
      this.graph = graph;
    }

    on() { return this; }
    setSetting() {}
    refresh() {}
    kill() {}
    getGraph() { return this.graph; }
    getCamera() {
      return {
        on: () => undefined,
        animatedReset: () => undefined,
      };
    }
  }
  return { default: TestSigma };
});

vi.mock("sigma/rendering", () => ({
  EdgeArrowProgram: class {},
  EdgeLineProgram: class {},
  NodeCircleProgram: class {},
}));

vi.mock("graphology-layout-forceatlas2/worker", () => ({
  default: class {
    start() {}
    stop() {}
    kill() {}
  },
}));

Object.defineProperty(window.navigator, "clipboard", {
  value: {
    writeText: vi.fn().mockResolvedValue(undefined),
  },
  configurable: true,
});

class TestResizeObserver implements ResizeObserver {
  private readonly callback: ResizeObserverCallback;

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback;
  }

  observe(target: Element) {
    this.callback([
      {
        target,
        contentRect: {
          x: 0,
          y: 0,
          width: 640,
          height: 240,
          top: 0,
          right: 640,
          bottom: 240,
          left: 0,
          toJSON: () => ({}),
        },
        borderBoxSize: [],
        contentBoxSize: [],
        devicePixelContentBoxSize: [],
      },
    ], this);
  }

  unobserve() {}

  disconnect() {}
}

Object.defineProperty(window, "ResizeObserver", {
  value: TestResizeObserver,
  configurable: true,
});
