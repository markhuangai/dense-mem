(() => {
  const root = document.documentElement;
  const system = matchMedia("(prefers-color-scheme: dark)");
  const storageKey = "dense-mem-docs-theme";
  let preference = "system";
  let control;

  function apply() {
    if (control) control.value = preference;
    const theme = preference === "system" ? (system.matches ? "dark" : "light") : preference;
    if (root.dataset.theme === theme) return;
    root.dataset.theme = theme;
    document.dispatchEvent(new Event("docs-theme-change"));
  }

  function restore() {
    try {
      const saved = localStorage.getItem(storageKey);
      preference = saved === "light" || saved === "dark" ? saved : "system";
    } catch {
      // Storage access may be blocked by browser privacy settings.
    }
    apply();
  }

  restore();
  system.addEventListener("change", apply);
  window.addEventListener("storage", event => {
    if (event.key === storageKey || event.key === null) restore();
  });
  window.addEventListener("pageshow", event => {
    if (event.persisted) restore();
  });
  document.addEventListener("DOMContentLoaded", () => {
    control = document.querySelector("[data-theme-control]");
    control.value = preference;
    control.hidden = false;
    control.addEventListener("change", () => {
      preference = control.value;
      try {
        if (preference === "system") localStorage.removeItem(storageKey);
        else localStorage.setItem(storageKey, preference);
      } catch {
        // The selected theme still applies for this page when storage is unavailable.
      }
      apply();
    });
  });
})();
