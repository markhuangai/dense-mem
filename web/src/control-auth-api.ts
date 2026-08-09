import { requestJson } from "./http";

export type ControlIdentityProvider = {
  id: string;
  name: string;
  kind: string;
};

export async function listControlIdentityProviders(): Promise<ControlIdentityProvider[]> {
  const payload = await requestJson<{ data: ControlIdentityProvider[] }>("/control/auth/providers", { credentials: "include" });
  return payload.data;
}
