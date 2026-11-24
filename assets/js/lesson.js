const playbackDelay = 5000;
const views = ["story", "system", "data"];
const motion = matchMedia("(prefers-reduced-motion: reduce)");

function measure(board) {
  const pieces = board?.querySelectorAll("[data-piece], [data-fragment]") ?? [];
  return new Map(Array.from(pieces, piece => [piece.dataset.piece ?? "fragment-" + piece.dataset.fragment, piece.getBoundingClientRect()]));
}

function animateBoard(board, origins) {
  for (const piece of board.querySelectorAll("[data-piece]")) {
    const origin = origins.get(piece.dataset.piece) ?? origins.get(piece.dataset.origin);
    const target = piece.getBoundingClientRect();
    if (origin) {
      const dx = origin.left + (origin.width - target.width) / 2 - target.left;
      const dy = origin.top - target.top;
      if (Math.abs(dx) + Math.abs(dy) > 3) {
        piece.animate([{transform: `translate(${dx}px, ${dy}px)`}, {transform: "none"}],
          {duration: 560, easing: "cubic-bezier(.22, 1, .36, 1)"});
      }
    } else {
      piece.animate([{opacity: .35, transform: "scale(.97)"}, {opacity: 1, transform: "none"}],
        {duration: 320, easing: "ease-out"});
    }
  }
}

function setupDetailPreview(root, onReveal) {
  const preview = root.querySelector("[data-detail-preview]");
  const supportsHover = matchMedia("(any-hover: hover)");
  const triggerSelector = "[data-preview-trigger]";
  let active;
  let timer;

  function hide() {
    if (!active) return;
    clearTimeout(timer);
    preview.hidden = true;
    active = undefined;
  }

  function reveal(trigger, pointerX) {
    if (trigger === active) return;
    hide();
    if (trigger.matches("details[open] > summary")) return;
    active = trigger;
    const show = () => {
      preview.textContent = document.getElementById(trigger.getAttribute("aria-describedby")).textContent;
      preview.hidden = false;
      const bounds = trigger.getBoundingClientRect();
      const left = Math.max(12, Math.min(pointerX ?? bounds.left, innerWidth - preview.offsetWidth - 12));
      const height = preview.offsetHeight;
      const placeBelow = trigger.matches("a") && bounds.bottom + height <= innerHeight - 12;
      const top = !placeBelow && bounds.top - height >= 12 ? bounds.top - height : bounds.bottom;
      preview.style.left = `${left}px`;
      preview.style.top = `${Math.max(12, Math.min(top, innerHeight - height - 12))}px`;
      onReveal();
    };
    if (pointerX === undefined) show();
    else timer = setTimeout(show, 160);
  }

  root.addEventListener("pointerover", event => {
    if (!supportsHover.matches || event.pointerType === "touch") return;
    const trigger = event.target.closest(triggerSelector);
    if (trigger && !trigger.contains(event.relatedTarget)) reveal(trigger, event.clientX);
  });
  root.addEventListener("pointerout", event => {
    if (!active || (!active.contains(event.target) && !preview.contains(event.target))) return;
    if (active.contains(event.relatedTarget) || (!preview.hidden && preview.contains(event.relatedTarget))) return;
    if (!active.matches(":focus-visible")) hide();
  });
  root.addEventListener("focusin", event => {
    if (event.target.matches(`${triggerSelector}:focus-visible`)) reveal(event.target);
  });
  root.addEventListener("focusout", event => {
    if (event.target === active && !(supportsHover.matches && (active.matches(":hover") || preview.matches(":hover")))) hide();
  });
  root.addEventListener("click", event => {
    if (!preview.contains(event.target)) hide();
  });
  document.addEventListener("keydown", event => {
    if (event.key === "Escape" && active) hide();
  });
  window.addEventListener("resize", hide);
  window.addEventListener("scroll", hide, {passive: true});
  return hide;
}

export function setupLesson(root) {
  const frames = Array.from(root.querySelectorAll("[data-frame]"));
  const version = root.querySelector("[data-alternative] [data-version]")?.dataset.version;
  const storyLinks = Array.from(root.querySelectorAll("[data-frame-link]"));
  const viewLinks = Array.from(root.querySelectorAll("[data-change-view]"));
  const play = root.querySelector("[data-play]");
  const previous = root.querySelector("[data-previous]");
  const next = root.querySelector("[data-next-frame]");
  const transcriptButton = root.querySelector("[data-transcript]");
  const scenarioButton = root.querySelector("[data-scenario]");
  const status = root.querySelector("[data-lesson-status]");
  let state;
  let transcript = false;
  let timer = 0;
  const hidePreview = setupDetailPreview(root, () => { if (timer) stop(); });

  function address({index, view, scenario, field}) {
    const url = new URL(location.href);
    url.hash = frames[index].id;
    for (const [key, value] of Object.entries({view: view === "story" ? "" : view, scenario, field})) {
      if (value) url.searchParams.set(key, value);
      else url.searchParams.delete(key);
    }
    return url;
  }

  function readAddress() {
    const url = new URL(location.href);
    const view = url.searchParams.get("view");
    const scenario = url.searchParams.get("scenario");
    return {
      index: Math.max(0, frames.findIndex(frame => "#" + frame.id === url.hash)),
      view: views.includes(view) ? view : "story",
      scenario: scenario === version ? version : "",
      field: url.searchParams.get("field") ?? ""
    };
  }

  function visibleVersion(frame, scenario) {
    return frame.querySelector(`[data-version="${scenario || "default"}"]`);
  }

  function show(update, {historyMethod, move = false} = {}) {
    hidePreview();
    const activeVersion = visibleVersion(frames[update.index], update.scenario);
    const mode = activeVersion.querySelector(`[data-view="${update.view}"]`);
    const fields = Array.from(activeVersion.querySelectorAll("[data-property]"));
    const field = fields.find(item => item.dataset.property === update.field) ?? fields[0];
    const animate = move && !motion.matches && !transcript;
    const oldMode = state && visibleVersion(frames[state.index], state.scenario).querySelector(`[data-view="${state.view}"]`);
    const origins = animate ? measure(oldMode) : new Map();
    root.getAnimations({subtree: true}).forEach(animation => animation.cancel());

    state = {...update, field: update.field && field ? field.dataset.property : ""};
    if (historyMethod) history[historyMethod](null, "", address(state));
    for (const [index, frame] of frames.entries()) {
      frame.hidden = !transcript && index !== state.index;
      const alternative = frame.querySelector("[data-alternative]");
      if (alternative) {
        alternative.hidden = !state.scenario;
        alternative.open = Boolean(state.scenario);
        frame.querySelector('[data-version="default"]').hidden = Boolean(state.scenario);
      }
      for (const panel of frame.querySelectorAll("[data-view]")) panel.hidden = panel.dataset.view !== state.view;
    }
    if (!transcript) {
      for (const item of fields) item.open = item === field;
    }
    for (const quote of mode.querySelectorAll("[data-highlight-for]")) {
      quote.classList.toggle("is-highlighted", quote.dataset.highlightFor.split(" ").includes(field?.dataset.property));
    }
    for (const link of viewLinks) {
      const view = link.dataset.changeView;
      link.href = address({...state, view});
      if (view === state.view) link.setAttribute("aria-current", "true");
      else link.removeAttribute("aria-current");
    }
    for (const link of storyLinks) {
      const index = frames.findIndex(frame => frame.id === link.dataset.frameLink);
      link.href = address({...state, index, field: ""});
      if (index === state.index && link.closest(".lesson-progress")) link.setAttribute("aria-current", "step");
      else link.removeAttribute("aria-current");
    }
    if (scenarioButton) scenarioButton.setAttribute("aria-pressed", String(Boolean(state.scenario)));
    previous.disabled = state.index === 0 || transcript;
    next.disabled = state.index === frames.length - 1 || transcript;
    play.disabled = transcript;
    play.textContent = timer ? "Pause" : state.index === frames.length - 1 ? "Play again" : "Play";
    status.textContent = frames[state.index].querySelector("h2").textContent + ". " + viewLinks.find(link => link.dataset.changeView === state.view).textContent + ".";

    if (!animate) return;
    if (state.view === "data" && field) {
      field.querySelector("[data-field-transfer]").animate([
        {opacity: .4, transform: "translateX(-12px)"}, {opacity: 1, transform: "none"}
      ], {duration: 330, easing: "ease-out"});
    } else {
      animateBoard(mode, origins);
    }
  }

  function stop() {
    clearTimeout(timer);
    timer = 0;
    play.textContent = state.index === frames.length - 1 ? "Play again" : "Play";
  }

  function go(update) {
    stop();
    show({...state, ...update}, {historyMethod: "pushState", move: true});
  }

  function tick() {
    const index = state.index + 1;
    if (index >= frames.length - 1) stop();
    else timer = setTimeout(tick, playbackDelay);
    show({...state, index, field: ""}, {historyMethod: "replaceState", move: true});
  }

  previous.addEventListener("click", () => go({index: state.index - 1, field: ""}));
  next.addEventListener("click", () => go({index: state.index + 1, field: ""}));
  play.addEventListener("click", () => {
    if (timer) { stop(); return; }
    const index = state.index === frames.length - 1 ? 0 : state.index;
    timer = setTimeout(tick, playbackDelay);
    show({...state, index, field: ""}, {historyMethod: "pushState", move: true});
  });
  transcriptButton.addEventListener("click", () => {
    stop();
    transcript = !transcript;
    transcriptButton.setAttribute("aria-pressed", String(transcript));
    transcriptButton.textContent = transcript ? "Return to playback" : "Read all frames";
    show(state);
  });
  scenarioButton?.addEventListener("click", () => go({scenario: state.scenario ? "" : version, field: ""}));
  root.addEventListener("click", event => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const view = event.target.closest("[data-change-view]");
    const frame = event.target.closest("[data-frame-link]");
    const property = event.target.closest("[data-property] > summary")?.parentElement;
    if (view) {
      event.preventDefault();
      if (view.dataset.changeView !== state.view) go({view: view.dataset.changeView});
    } else if (frame && !transcript) {
      event.preventDefault();
      const index = frames.findIndex(item => item.id === frame.dataset.frameLink);
      go({index, field: ""});
      if (!frame.closest(".lesson-progress")) {
        const heading = frames[index].querySelector("h2");
        heading.tabIndex = -1;
        heading.focus();
      }
    } else if (property && !transcript) {
      event.preventDefault();
      go({field: property.dataset.property});
    }
  });
  function restore() {
    stop();
    show(readAddress());
  }
  window.addEventListener("popstate", restore);
  window.addEventListener("hashchange", restore);
  motion.addEventListener("change", () => show(state));
  document.addEventListener("visibilitychange", () => { if (document.hidden && timer) stop(); });
  show(readAddress());
  root.dataset.ready = "true";
  for (const controls of root.querySelectorAll("[data-interactive]")) controls.hidden = false;
}
