import { useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { PortalEnvironment, portalHref, usePortalNavigation, useUnsavedChanges } from "./navigation";

function Editor() {
  const [text, setText] = useState("");
  useUnsavedChanges(Boolean(text));
  return <input aria-label="Unsaved note" value={text} onChange={(event) => setText(event.target.value)} />;
}
function Workspace() {
  const { parameters, navigate, transition } = usePortalNavigation();
  const section = parameters.get("section") ?? "edit";
  const [localTab, setLocalTab] = useState("one");
  return <>
    <output aria-label="Location">{section}</output>
    <button onClick={() => void navigate({ section: "edit" })}>Editor</button>
    <button onClick={() => void navigate({ section: "logs", team_id: "team-a" })}>Logs</button>
    <button onClick={() => void navigate({ tab: "two" })}>Retained form tab</button>
    <button onClick={() => void transition(async () => false)}>Failed refresh</button>
    <button onClick={() => setLocalTab("two")}>Local tab</button><span>{localTab}</span>
    {section === "edit" && <Editor />}
  </>;
}

beforeEach(() => window.history.replaceState(null, "", "/"));

describe("portal navigation", () => {
  it("keeps only supported navigation parameters and never serializes form text", () => {
    window.history.replaceState(null, "", "/?section=teams&team_id=a&token=secret&query=private#content");
    expect(portalHref({ team_tab: "remember-attempts", remember_view: "calls", invocation_id: "call-a" })).toBe("/?section=teams&team_id=a&team_tab=remember-attempts&remember_view=calls&invocation_id=call-a#content");
    expect(portalHref({ section: "logs", team_id: null })).toBe("/?section=logs#content");
  });

  it("cancels navigation without losing edits, then accepts explicit discard", async () => {
    const user = userEvent.setup();
    render(<PortalEnvironment><Workspace /></PortalEnvironment>);
    await user.type(screen.getByLabelText("Unsaved note"), "private draft");
    await user.click(screen.getByRole("button", { name: "Logs" }));
    expect(screen.getByRole("alertdialog")).toHaveAccessibleName("Discard unsaved changes?");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByLabelText("Unsaved note")).toHaveValue("private draft");
    expect(window.location.search).toBe("");
    await user.click(screen.getByRole("button", { name: "Logs" }));
    await user.click(screen.getByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(screen.getByLabelText("Location")).toHaveTextContent("logs"));
    expect(window.location.search).toBe("?section=logs&team_id=team-a");
    expect(window.location.href).not.toContain("private");
  });

  it("keeps the guard when a form remains mounted or an operation fails", async () => {
    const user = userEvent.setup();
    render(<PortalEnvironment><Workspace /></PortalEnvironment>);
    await user.type(screen.getByLabelText("Unsaved note"), "edited");
    await user.click(screen.getByRole("button", { name: "Retained form tab" }));
    await user.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(screen.getByLabelText("Unsaved note")).toHaveValue("edited");
    await user.click(screen.getByRole("button", { name: "Failed refresh" }));
    await user.click(screen.getByRole("button", { name: "Discard changes" }));
    const event = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    await user.click(screen.getByRole("button", { name: "Logs" }));
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  });

  it("restores the accepted history entry after a cancelled Back operation", async () => {
    const user = userEvent.setup();
    render(<PortalEnvironment><Workspace /></PortalEnvironment>);
    await user.click(screen.getByRole("button", { name: "Logs" }));
    await user.click(screen.getByRole("button", { name: "Editor" }));
    await user.type(screen.getByLabelText("Unsaved note"), "keep");
    act(() => window.history.back());
    await screen.findByRole("alertdialog");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(window.location.search).toBe("?section=edit&team_id=team-a"));
    expect(screen.getByLabelText("Location")).toHaveTextContent("edit");
    expect(screen.getByLabelText("Unsaved note")).toHaveValue("keep");
    fireEvent.change(screen.getByLabelText("Unsaved note"), { target: { value: "" } });
    act(() => window.history.back());
    await waitFor(() => expect(screen.getByLabelText("Location")).toHaveTextContent("logs"));
    act(() => window.history.forward());
    await waitFor(() => expect(screen.getByLabelText("Location")).toHaveTextContent("edit"));
  });
});
