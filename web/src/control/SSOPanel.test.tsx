import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ControlApi, SSOGroupMapping, SSOProvider, Team } from "../api";
import { SSOPanel } from "./SSOPanel";

const team: Team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Default",
  description: "",
  metadata: null,
  config: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

const providerA = ssoProvider("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "Provider A");
const providerB = ssoProvider("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "Provider B");

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("SSOPanel", () => {
  it("ignores stale mapping responses after switching providers", async () => {
    const providerAMappings = deferred<SSOGroupMapping[]>();
    const api = {
      listSSOProviders: vi.fn(async () => [providerA, providerB]),
      listSSOGroupMappings: vi.fn((providerId: string) => {
        if (providerId === providerA.id) {
          return providerAMappings.promise;
        }
        return Promise.resolve([ssoMapping(providerB, "group-b")]);
      }),
    } as unknown as ControlApi;

    render(<SSOPanel api={api} teams={[team]} />);

    await screen.findByText("Provider A");
    await waitFor(() => expect(api.listSSOGroupMappings).toHaveBeenCalledWith(providerA.id));
    await userEvent.click(screen.getByRole("button", { name: "Edit Provider B" }));

    expect(await screen.findByText("group-b")).toBeInTheDocument();
    await act(async () => {
      providerAMappings.resolve([ssoMapping(providerA, "group-a")]);
      await providerAMappings.promise;
    });

    expect(screen.getByText("group-b")).toBeInTheDocument();
    expect(screen.queryByText("group-a")).not.toBeInTheDocument();
  });
});

function ssoProvider(id: string, name: string): SSOProvider {
  return {
    id,
    name,
    kind: "generic_oidc",
    issuer_url: `https://${name.toLowerCase().split(" ").join("-")}.example.com`,
    client_id: `${id}-client`,
    client_secret_env: "",
    scopes: ["openid", "profile", "email"],
    group_claims: ["groups"],
    groups_endpoint: "",
    groups_scopes: [],
    enabled: true,
    created_at: "2026-05-01T12:00:00Z",
    updated_at: "2026-05-01T12:00:00Z",
  };
}

function ssoMapping(provider: SSOProvider, groupId: string): SSOGroupMapping {
  return {
    id: `${provider.id}-${groupId}`,
    provider_id: provider.id,
    team_id: team.id,
    team_name: team.name,
    group_id: groupId,
    group_name: "",
    scopes: ["read"],
    role: "member",
    enabled: true,
    created_at: "2026-05-01T12:00:00Z",
    updated_at: "2026-05-01T12:00:00Z",
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
