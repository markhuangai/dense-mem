import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

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
