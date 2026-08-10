import { beforeEach, describe, expect, it, vi } from "vitest";
import { listControlIdentityProviders } from "./control-auth-api";

describe("control auth API", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("returns identity providers from the standard envelope", async () => {
    const providers = [{ id: "entra", name: "Entra ID", kind: "entra" }];
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: providers }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(listControlIdentityProviders()).resolves.toEqual(providers);
    expect(fetchMock).toHaveBeenCalledWith("/control/auth/providers", expect.objectContaining({ credentials: "include" }));
  });

  it("returns a bounded error when the success payload is missing", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 200 })));

    await expect(listControlIdentityProviders()).rejects.toMatchObject({
      name: "ApiError",
      status: 502,
      message: "Unexpected identity provider response",
    });
  });
});
