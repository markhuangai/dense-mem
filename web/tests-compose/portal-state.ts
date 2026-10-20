import { randomUUID } from "node:crypto";
import { expect, test, type APIRequestContext } from "@playwright/test";
import { confirmAction, navigate } from "../tests-support/portal";

const controlURL = process.env.DENSE_MEM_CONTROL_URL!.replace(/\/$/, "");
const userURL = process.env.DENSE_MEM_USER_URL!.replace(/\/$/, "");
const token = process.env.DENSE_MEM_CONTROL_TOKEN!;

async function control(request: APIRequestContext, path: string, data?: unknown, method = data ? "POST" : "GET") {
  const response = await request.fetch(`${controlURL}/control/api${path}`, { method, headers: { Authorization: `Bearer ${token}` }, ...(data ? { data } : {}) });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return (await response.json()).data;
}

for (const portal of ["Control", "Knowledge"] as const) {
  test(`${portal} refresh discards confirmed team edits and retains cancelled edits`, async ({ page, request }) => {
    const originalName = `Refresh ${portal} ${randomUUID()}`;
    const team = await control(request, "/teams", { name: originalName, description: "Original description" });
    try {
      if (portal === "Control") {
        await page.goto(`${controlURL}/?section=teams&team_id=${team.id}&team_tab=settings`);
        await page.getByLabel("Control token").fill(token);
        await page.getByRole("button", { name: "Unlock" }).click();
      } else {
        const credential = await control(request, `/teams/${team.id}/credentials`, { name: "Refresh manager", scopes: ["read", "write"], role: "manager", rate_limit: 300 });
        await page.goto(`${userURL}/ui/?section=team`);
        await page.getByLabel("API key").fill(credential.api_key);
        await page.getByRole("button", { name: "Sign in" }).click();
      }
      const name = page.getByLabel("Name", { exact: true });
      const description = page.getByLabel("Description", { exact: true });
      await expect(name).toHaveValue(originalName);
      await name.fill(`${originalName} edited`);
      await description.fill("Unsaved description");
      const refresh = page.getByRole("button", { name: portal === "Control" ? "Refresh teams" : "Refresh session", exact: true });
      await refresh.click();
      await confirmAction(page, "Cancel");
      await expect(name).toHaveValue(`${originalName} edited`);
      await expect(description).toHaveValue("Unsaved description");
      await refresh.click();
      await confirmAction(page, "Discard changes");
      await expect(name).toHaveValue(originalName);
      await expect(description).toHaveValue("Original description");
      await expect(name).toBeEnabled();
      await navigate(page, portal === "Control" ? "Logs" : "Search memory");
      await expect(page.getByRole("alertdialog")).toHaveCount(0);
      await expect(name).toHaveCount(0);
      await navigate(page, portal === "Control" ? "Team settings" : "Team");
      await expect(name).toHaveValue(originalName);
      await name.fill("Discard before changed server response");
      await control(request, `/teams/${team.id}`, { name: `${originalName} changed`, description: "Changed on server" }, "PATCH");
      await refresh.click();
      await confirmAction(page, "Discard changes");
      await expect(name).toHaveValue(`${originalName} changed`);
      await expect(description).toHaveValue("Changed on server");
      await navigate(page, portal === "Control" ? "Logs" : "Search memory");
      await expect(page.getByRole("alertdialog")).toHaveCount(0);
    } finally {
      await control(request, `/teams/${team.id}`, undefined, "DELETE");
    }
  });
}

test("Control mapping saves stay clean for a non-first team while unsaved fields still guard navigation", async ({ page, request }) => {
  const provider = await control(request, "/sso/providers", {
    name: `Mapping regression ${randomUUID()}`, kind: "generic_oidc", issuer_url: "https://mapping.example.test", tenant_id: "", identity_claim: "sub",
    client_id: "mapping-regression", client_secret_env: "", scopes: ["openid"], group_claims: ["groups"], enabled: false,
  });
  try {
    const teams = await control(request, "/teams");
    expect(teams.length).toBeGreaterThan(1);
    const target = teams[1];
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Identity & access");
    await page.getByRole("button", { name: `Edit ${provider.name}`, exact: true }).click();
    await page.getByRole("tab", { name: "Mappings", exact: true }).click();
    await page.getByLabel("Team", { exact: true }).selectOption(target.id);
    await navigate(page, "Logs");
    await expect(page.getByRole("heading", { name: "Operation Logs", exact: true })).toBeVisible();
    await expect(page.getByRole("alertdialog")).toHaveCount(0);
    await navigate(page, "Identity & access");
    await page.getByRole("button", { name: `Edit ${provider.name}`, exact: true }).click();
    await page.getByRole("tab", { name: "Mappings", exact: true }).click();
    await page.getByLabel("Team", { exact: true }).selectOption(target.id);
    await page.getByLabel("Group ID", { exact: true }).fill(`group-${randomUUID()}`);
    const saved = page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === `/control/api/sso/providers/${provider.id}/mappings`);
    await page.getByRole("button", { name: "Add", exact: true }).click();
    expect((await saved).status()).toBe(201);
    await expect(page.getByLabel("Group ID", { exact: true })).toHaveValue("");
    await expect(page.getByLabel("Team", { exact: true })).toHaveValue(target.id);
    await navigate(page, "Logs");
    await expect(page.getByRole("heading", { name: "Operation Logs", exact: true })).toBeVisible();
    await expect(page.getByRole("alertdialog")).toHaveCount(0);
    await navigate(page, "Identity & access");
    await page.getByRole("button", { name: `Edit ${provider.name}`, exact: true }).click();
    await page.getByRole("tab", { name: "Mappings", exact: true }).click();
    await page.getByLabel("Group ID", { exact: true }).fill("Keep this unsaved group");
    await navigate(page, "Logs");
    await confirmAction(page, "Cancel");
    await expect(page.getByLabel("Group ID", { exact: true })).toHaveValue("Keep this unsaved group");
  } finally {
    await control(request, `/sso/providers/${provider.id}`, undefined, "DELETE");
  }
});
