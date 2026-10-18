import { createContext, ReactNode, useCallback, useContext, useEffect, useId, useMemo, useRef, useState } from "react";
import { ConfirmationProvider, useConfirmation } from "./confirmation";

const parameterNames = ["section", "team_id", "team_tab", "tab", "logs_scope", "remember_view", "attempt_id", "invocation_id"] as const;
export type NavigationPatch = Partial<Record<typeof parameterNames[number], string | null>>;
type Navigation = {
  parameters: URLSearchParams;
  entryIndex: number;
  navigate: (patch: NavigationPatch, replace?: boolean) => Promise<boolean>;
  transition: (action: () => void | boolean | Promise<void | boolean>) => Promise<boolean>;
  registerDirty: (id: string, dirty: boolean) => void;
};
const NavigationContext = createContext<Navigation | null>(null);
const DirtyContext = createContext<(id: string, dirty: boolean) => void>(() => {});

export function portalHref(patch: NavigationPatch) {
  const url = new URL(window.location.href);
  for (const name of [...url.searchParams.keys()]) {
    if (!(parameterNames as readonly string[]).includes(name)) url.searchParams.delete(name);
  }
  for (const [name, value] of Object.entries(patch)) {
    if (value === null || value === "") url.searchParams.delete(name);
    else url.searchParams.set(name, value);
  }
  return `${url.pathname}${url.search}${url.hash}`;
}

function NavigationProvider({ children }: { children: ReactNode }) {
  const confirm = useConfirmation();
  const [search, setSearch] = useState(window.location.search);
  const dirtyForms = useRef(new Set<string>());
  const accepted = useRef({ url: window.location.href, index: Number(window.history.state?.denseMemIndex ?? 0) });
  const restoring = useRef(false);
  const popRequest = useRef(0);

  const registerDirty = useCallback((id: string, dirty: boolean) => {
    if (dirty) dirtyForms.current.add(id);
    else dirtyForms.current.delete(id);
  }, []);

  const transition = useCallback(async (action: () => void | boolean | Promise<void | boolean>) => {
    if (dirtyForms.current.size && !await confirm({ title: "Discard unsaved changes?", description: "Your unsaved edits will be lost when you leave this view.", confirmLabel: "Discard changes", destructive: true })) return false;
    if (await action() === false) return false;
    return true;
  }, [confirm]);

  const navigate = useCallback(async (patch: NavigationPatch, replace = false) => {
    const href = portalHref(patch);
    if (new URL(href, window.location.origin).href === accepted.current.url) return true;
    return transition(() => {
      const index = accepted.current.index + (replace ? 0 : 1);
      window.history[replace ? "replaceState" : "pushState"]({ ...window.history.state, denseMemIndex: index }, "", href);
      accepted.current = { url: window.location.href, index };
      setSearch(window.location.search);
    });
  }, [transition]);

  useEffect(() => {
    window.history.replaceState({ ...window.history.state, denseMemIndex: accepted.current.index }, "", window.location.href);
    const onPop = async () => {
      if (restoring.current) { restoring.current = false; return; }
      const request = ++popRequest.current;
      const nextURL = window.location.href;
      const nextIndex = Number(window.history.state?.denseMemIndex ?? accepted.current.index);
      const approved = !dirtyForms.current.size || await confirm({ title: "Discard unsaved changes?", description: "Your unsaved edits will be lost when you leave this view.", confirmLabel: "Discard changes", destructive: true });
      if (request !== popRequest.current) return;
      if (approved) {
        accepted.current = { url: nextURL, index: nextIndex };
        setSearch(new URL(nextURL).search);
      } else if (nextIndex !== accepted.current.index) {
        restoring.current = true;
        window.history.go(accepted.current.index - nextIndex);
      } else {
        window.history.replaceState({ ...window.history.state, denseMemIndex: accepted.current.index }, "", accepted.current.url);
      }
    };
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (dirtyForms.current.size) { event.preventDefault(); event.returnValue = ""; }
    };
    window.addEventListener("popstate", onPop);
    window.addEventListener("beforeunload", beforeUnload);
    return () => { window.removeEventListener("popstate", onPop); window.removeEventListener("beforeunload", beforeUnload); };
  }, [confirm]);

  const value = useMemo(() => ({ parameters: new URLSearchParams(search), entryIndex: accepted.current.index, navigate, transition, registerDirty }), [search, navigate, transition, registerDirty]);
  return <NavigationContext.Provider value={value}><DirtyContext.Provider value={registerDirty}>{children}</DirtyContext.Provider></NavigationContext.Provider>;
}

export function PortalEnvironment({ children }: { children: ReactNode }) {
  return <ConfirmationProvider><NavigationProvider>{children}</NavigationProvider></ConfirmationProvider>;
}

export function usePortalNavigation() {
  const navigation = useContext(NavigationContext);
  if (!navigation) throw new Error("Portal navigation requires PortalEnvironment.");
  return navigation;
}

export function useUnsavedChanges(dirty: boolean) {
  const id = useId();
  const register = useContext(DirtyContext);
  useEffect(() => { register(id, dirty); return () => register(id, false); }, [id, dirty, register]);
  return useCallback(() => register(id, false), [id, register]);
}

export function useViewTransition() {
  const navigation = useContext(NavigationContext);
  return navigation?.transition ?? (async (action: () => void | boolean | Promise<void | boolean>) => await action() !== false);
}
