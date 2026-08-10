import { ApiError, requestJson } from "./http";

export type ControlIdentityProvider = {
  id: string;
  name: string;
  kind: string;
};

export async function listControlIdentityProviders(): Promise<ControlIdentityProvider[]> {
  const payload = await requestJson<{ data: ControlIdentityProvider[] } | null>("/control/auth/providers", { credentials: "include" });
  if (!payload || !Array.isArray(payload.data)) {
    throw new ApiError(502, "Unexpected identity provider response");
  }
  return payload.data;
}
