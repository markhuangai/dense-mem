import { useCallback, useEffect, useRef } from "react";

const DEFAULT_POLL_INTERVAL_MS = 60_000;

export type VisiblePollLoader = (signal: AbortSignal) => Promise<void>;

export function useVisiblePolling(
  loader: VisiblePollLoader,
  dependencies: readonly unknown[],
  intervalMs = DEFAULT_POLL_INTERVAL_MS,
) {
  const loaderRef = useRef(loader);
  const inFlightRef = useRef<AbortController | null>(null);
  loaderRef.current = loader;

  const refresh = useCallback(() => {
    if (inFlightRef.current) {
      return Promise.resolve();
    }
    const controller = new AbortController();
    inFlightRef.current = controller;
    return loaderRef.current(controller.signal).finally(() => {
      if (inFlightRef.current === controller) {
        inFlightRef.current = null;
      }
    });
  }, []);

  const cancelInFlight = useCallback(() => {
    const controller = inFlightRef.current;
    if (!controller) {
      return;
    }
    inFlightRef.current = null;
    controller.abort();
  }, []);

  useEffect(() => {
    const handleVisibility = () => {
      if (document.visibilityState === "visible") {
        void refresh();
      } else {
        cancelInFlight();
      }
    };

    handleVisibility();
    const interval = window.setInterval(handleVisibility, intervalMs);
    document.addEventListener("visibilitychange", handleVisibility);
    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", handleVisibility);
      cancelInFlight();
    };
  }, [cancelInFlight, intervalMs, refresh, ...dependencies]);

  return refresh;
}
