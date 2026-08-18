import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UserPortalApp } from "./App";
import { mockSSOUserFetch, ssoSessions } from "./App.test-helpers";

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("UserPortalApp private-memory polling", () => {
  it("reuses an idempotency key until submission is accepted", async () => {
    const { initial, switched } = ssoSessions();
    const fetchMock = mockSSOUserFetch(initial, switched, { profileErasureFailures: 1 });
    prepareErasureInteraction();

    render(<UserPortalApp />);
    await openCredentialPanel();
    const erase = screen.getByRole("button", { name: "Erase private memory" });
    await userEvent.click(erase);
    expect(await screen.findByRole("alert")).toHaveTextContent("profile erasure unavailable");
    await userEvent.click(erase);
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Profile-private erasure completed."));

    const submissions = fetchMock.mock.calls.filter(([input, init]) => String(input) === "/ui/api/sso/private-memory" && init?.method === "DELETE");
    expect(submissions).toHaveLength(2);
    const firstKey = (submissions[0][1]?.headers as Record<string, string>)["Idempotency-Key"];
    const secondKey = (submissions[1][1]?.headers as Record<string, string>)["Idempotency-Key"];
    expect(firstKey).toBe(secondKey);
  });

  it("stops polling and reports a terminal failure", async () => {
    const { initial, switched } = ssoSessions();
    const fetchMock = mockSSOUserFetch(initial, switched, { profileErasurePollStatus: "failed" });
    prepareErasureInteraction();

    render(<UserPortalApp />);
    await openCredentialPanel();
    await userEvent.click(screen.getByRole("button", { name: "Erase private memory" }));

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Profile-private erasure failed (manifest_mismatch)."));
    const polls = fetchMock.mock.calls.filter(([input, init]) => String(input).includes("/ui/api/private-memory/erasures/") && init?.method === "GET");
    expect(polls).toHaveLength(1);
  });

  it("bounds polling at 40 attempts", async () => {
    const { initial, switched } = ssoSessions();
    const fetchMock = mockSSOUserFetch(initial, switched, { profileErasurePollStatus: "processing" });
    prepareErasureInteraction();

    render(<UserPortalApp />);
    await openCredentialPanel();
    await userEvent.click(screen.getByRole("button", { name: "Erase private memory" }));

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("status polling timed out after 40 attempts"));
    const polls = fetchMock.mock.calls.filter(([input, init]) => String(input).includes("/ui/api/private-memory/erasures/") && init?.method === "GET");
    expect(polls).toHaveLength(40);
  });

  it("aborts polling when the credential panel unmounts", async () => {
    const { initial, switched } = ssoSessions();
    const fetchMock = mockSSOUserFetch(initial, switched);
    const defaultFetch = fetchMock.getMockImplementation();
    let pollSignal: AbortSignal | undefined;
    fetchMock.mockImplementation(async (input, init) => {
      if (String(input).includes("/ui/api/private-memory/erasures/") && init?.method === "GET") {
        pollSignal = init.signal as AbortSignal;
        return new Promise<Response>((_resolve, reject) => {
          pollSignal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
        });
      }
      return defaultFetch!(input, init);
    });
    prepareErasureInteraction();

    const { unmount } = render(<UserPortalApp />);
    await openCredentialPanel();
    const erase = screen.getByRole("button", { name: "Erase private memory" });
    await userEvent.click(erase);
    await waitFor(() => expect(pollSignal).toBeDefined());
    expect(erase).toBeDisabled();

    unmount();
    expect(pollSignal?.aborted).toBe(true);
  });
});

async function openCredentialPanel() {
  expect(await screen.findByLabelText("Current workspace")).toHaveTextContent("Research Team");
  await userEvent.click(screen.getByRole("button", { name: /my credential/i }));
}

function prepareErasureInteraction() {
  vi.spyOn(window, "confirm").mockReturnValue(true);
  const originalSetTimeout = window.setTimeout.bind(window);
  vi.spyOn(window, "setTimeout").mockImplementation(((handler: TimerHandler, timeout?: number, ...args: unknown[]) => {
    if (timeout === 500 && typeof handler === "function") {
      queueMicrotask(() => handler(...args));
      return 1;
    }
    return originalSetTimeout(handler, timeout, ...args);
  }) as typeof window.setTimeout);
}
