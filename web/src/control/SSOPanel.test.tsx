import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ControlApi, SSOGroupMapping, SSOProvider, Team } from "../api";
import { SSOPanel } from "./SSOPanel";
import { PortalEnvironment, usePortalNavigation } from "../ui/navigation";

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
const secondTeam = { ...team, id: "22222222-2222-4222-8222-222222222222", name: "Research" };

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("SSOPanel", () => {
  it.each([false, true])("keeps a non-first team mapping clean after selection or save (save=%s)", async (save) => {
    const api = await renderMappingWorkspace();
    await userEvent.click(screen.getByRole("tab", { name: "Mappings" }));
    await userEvent.selectOptions(screen.getByLabelText("Team", { exact: true }), secondTeam.id);
    if (save) {
      await userEvent.type(screen.getByLabelText("Group ID"), "saved-group");
      await userEvent.click(screen.getByRole("button", { name: "Add" }));
      await waitFor(() => expect(screen.getByLabelText("Group ID")).toHaveValue(""));
      expect(api.createSSOGroupMapping).toHaveBeenCalledWith(providerA.id, expect.objectContaining({ team_id: secondTeam.id, group_id: "saved-group" }));
    }
    expect(screen.getByLabelText("Team", { exact: true })).toHaveValue(secondTeam.id);
    await userEvent.click(screen.getByRole("button", { name: "Leave mappings" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it.each([false, true])("guards a provider switch with unsaved mapping fields (discard=%s)", async (discard) => {
    await renderMappingWorkspace();
    await userEvent.click(screen.getByRole("tab", { name: "Mappings" }));
    await userEvent.type(screen.getByLabelText("Group ID"), "unsaved-group");
    await userEvent.click(screen.getByRole("tab", { name: "Providers" }));
    await userEvent.click(screen.getByRole("button", { name: "Edit Provider B" }));
    await userEvent.click(await screen.findByRole("button", { name: discard ? "Discard changes" : "Cancel" }));
    await waitFor(() => expect(screen.getByLabelText("Name", { exact: true })).toHaveValue(discard ? providerB.name : providerA.name));
    await userEvent.click(screen.getByRole("tab", { name: "Mappings" }));
    expect(screen.getByLabelText("Group ID")).toHaveValue(discard ? "" : "unsaved-group");
  });

  it("still guards navigation when provider fields are dirty", async () => {
    await renderMappingWorkspace();
    await userEvent.type(screen.getByLabelText("Name", { exact: true }), " edited");
    await userEvent.click(screen.getByRole("button", { name: "Leave mappings" }));
    expect(await screen.findByRole("alertdialog")).toBeInTheDocument();
  });
	it("renders and saves the protected-resource contract", async () => {
		const oauthProvider: SSOProvider = {
			...providerA,
			protected_resource: {
				enabled: true,
				audiences: ["api://dense-mem"],
				jwks_source: "static",
				jwks_uri: "https://provider-a.example.com/jwks.json",
				algorithms: ["RS256", "ES256"],
				scope_claim: "scp",
				scope_mappings: [{ external_scope: "densemem.read", internal_scopes: ["read"] }],
				team_claim: "dense_mem_team_id",
			},
		};
		const api = {
			listSSOProviders: vi.fn(async () => [oauthProvider]),
			listSSOGroupMappings: vi.fn(async () => []),
			updateSSOProvider: vi.fn(async (_providerId, input) => ({ ...oauthProvider, ...input })),
			getDirectoryConnector: vi.fn(async () => { throw new ApiError(404, "not found"); }),
			listControlAdminGroups: vi.fn(async () => []),
		} as unknown as ControlApi;

		render(<SSOPanel api={api} teams={[team]} />);

		await waitFor(() => expect(screen.getByLabelText("Accept OAuth JWTs on MCP")).toBeChecked());
		expect(screen.getByLabelText("Allowed audiences")).toHaveValue("api://dense-mem");
		expect(screen.getByLabelText("JWKS source")).toHaveValue("static");
		expect(screen.getByLabelText("JWKS URL")).toHaveValue("https://provider-a.example.com/jwks.json");
		expect(screen.getByLabelText("Signature algorithms")).toHaveValue("RS256, ES256");
		expect(screen.getByLabelText("Scope claim")).toHaveValue("scp");
		expect(screen.getByLabelText("Team claim")).toHaveValue("dense_mem_team_id");
		expect(screen.getByLabelText("External scope")).toHaveValue("densemem.read");

		await userEvent.clear(screen.getByLabelText("Allowed audiences"));
		await userEvent.type(screen.getByLabelText("Allowed audiences"), "api://dense-mem-v2");
		await userEvent.click(screen.getByRole("button", { name: "Save provider" }));
		await waitFor(() => expect(api.updateSSOProvider).toHaveBeenCalledWith(providerA.id, expect.objectContaining({
			protected_resource: expect.objectContaining({
				enabled: true,
				audiences: ["api://dense-mem-v2"],
				scope_mappings: [{ external_scope: "densemem.read", internal_scopes: ["read"] }],
			}),
		})));
	});

	it("keeps OAuth scope mapping inputs mounted while editing and removing rows", async () => {
		const api = {
			listSSOProviders: vi.fn(async () => [providerA]),
			listSSOGroupMappings: vi.fn(async () => []),
			getDirectoryConnector: vi.fn(async () => { throw new ApiError(404, "not found"); }),
			listControlAdminGroups: vi.fn(async () => []),
		} as unknown as ControlApi;

		render(<SSOPanel api={api} teams={[team]} />);
		await screen.findByText("Provider A");

		await userEvent.click(screen.getByRole("button", { name: "Add mapping" }));
		await userEvent.click(screen.getByRole("button", { name: "Add mapping" }));
		const inputs = screen.getAllByLabelText("External scope");
		await userEvent.type(inputs[0], "memory.read");
		await userEvent.type(inputs[1], "memory.write");
		expect(inputs[0]).toHaveValue("memory.read");
		expect(inputs[1]).toHaveValue("memory.write");

		await userEvent.click(screen.getByRole("button", { name: "Remove OAuth scope mapping 1" }));
		expect(screen.getByLabelText("External scope")).toBe(inputs[1]);
		expect(inputs[1]).toHaveValue("memory.write");
	});

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
      getDirectoryConnector: vi.fn(async () => {
        throw new ApiError(404, "not found");
      }),
      listControlAdminGroups: vi.fn(async () => []),
    } as unknown as ControlApi;

    render(<SSOPanel api={api} teams={[team]} />);

    await screen.findByText("Provider A");
    await waitFor(() => expect(api.listSSOGroupMappings).toHaveBeenCalledWith(providerA.id));
    await userEvent.click(screen.getByRole("button", { name: "Edit Provider B" }));

    await userEvent.click(screen.getByRole("tab", { name: "Mappings" }));
    expect(await screen.findByText("group-b")).toBeInTheDocument();
    await act(async () => {
      providerAMappings.resolve([ssoMapping(providerA, "group-a")]);
      await providerAMappings.promise;
    });

    expect(screen.getByText("group-b")).toBeInTheDocument();
    expect(screen.queryByText("group-a")).not.toBeInTheDocument();
  });
});

async function renderMappingWorkspace() {
  const api = {
    listSSOProviders: vi.fn(async () => [providerA, providerB]),
    listSSOGroupMappings: vi.fn(async () => []),
    createSSOGroupMapping: vi.fn(async () => ({ ...ssoMapping(providerA, "saved-group"), team_id: secondTeam.id })),
    getDirectoryConnector: vi.fn(async () => { throw new ApiError(404, "not found"); }),
    listControlAdminGroups: vi.fn(async () => []),
  } as unknown as ControlApi;
  function Workspace() {
    const { navigate, parameters } = usePortalNavigation();
    return <><button onClick={() => void navigate({ section: parameters.get("section") === "logs" ? "config" : "logs" })}>Leave mappings</button><SSOPanel api={api} teams={[team, secondTeam]} /></>;
  }
  render(<PortalEnvironment><Workspace /></PortalEnvironment>);
  await waitFor(() => expect(screen.getByLabelText("Name", { exact: true })).toHaveValue(providerA.name));
  return api;
}

function ssoProvider(id: string, name: string): SSOProvider {
  return {
    id,
    name,
    kind: "generic_oidc",
    issuer_url: `https://${name.toLowerCase().split(" ").join("-")}.example.com`,
    tenant_id: "",
    identity_claim: "sub",
    client_id: `${id}-client`,
    client_secret_env: "",
    scopes: ["openid", "profile", "email"],
    group_claims: ["groups"],
		groups_endpoint: "",
		groups_scopes: [],
		protected_resource: {
			enabled: false,
			audiences: [],
			jwks_source: "discovery",
			jwks_uri: "",
			algorithms: ["RS256"],
			scope_claim: "scope",
			scope_mappings: [],
			team_claim: "",
		},
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
    origin: "manual",
    retired_at: null,
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
