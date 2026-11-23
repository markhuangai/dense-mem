const motion = matchMedia("(prefers-reduced-motion: reduce)");

function measure(root) {
  if (!root || motion.matches) return new Map();
  return new Map(Array.from(root.querySelectorAll("[data-track]"), node => [node.dataset.track, node.getBoundingClientRect()]));
}

function animate(root, direction = 1, positions = new Map()) {
  if (!root || motion.matches) return;
  root.getAnimations({subtree: true}).forEach(animation => animation.cancel());
  const ease = "cubic-bezier(.2,.72,.26,1)";
  const move = (node, frames, duration, delay = 0) => node.animate(frames, {duration, delay, easing: ease, fill: "backwards"});
  const scene = root.dataset.scene;
  for (const node of root.querySelectorAll("[data-track]")) {
    const key = node.dataset.track;
    let origin = positions.get(key) ?? positions.get("words-" + key) ?? positions.get("vectors-" + key);
    if (scene === "partition" && (key === "A" || key === "B")) {
      origin = root.querySelector('[data-track="text-' + key + '"]')?.getBoundingClientRect();
    }
    if (scene === "publish" && /^E\d+$/.test(key)) {
      origin = root.querySelector('[data-track="source-' + key + '"]')?.getBoundingClientRect();
    }
    if (!origin) continue;
    const rect = node.getBoundingClientRect();
    const x = origin.left + (origin.width - rect.width) / 2 - rect.left;
    const y = origin.top + (origin.height - rect.height) / 2 - rect.top;
    if (Math.abs(x) + Math.abs(y) > 4) {
      move(node, [{transform: "translate(" + x + "px," + y + "px)", opacity: .4}, {transform: "none", opacity: 1}], 510, 65);
    }
  }
  const movers = Array.from(root.querySelectorAll("[data-move]"));
  if (root.matches("[data-move]")) movers.unshift(root);
  movers.forEach((node, index) => {
    move(node, [{opacity: .3, transform: "translateY(" + (-10 * direction) + "px)"}, {opacity: 1, transform: "none"}], 390, Math.min(index, 5) * 60);
  });
  for (const node of root.querySelectorAll("[data-wire]")) {
    move(node, [{transform: node.classList.contains("fork") ? "scaleX(0)" : "scaleY(0)"}, {transform: "none"}], 360, 90);
  }
  for (const bar of root.querySelectorAll(".score-bar")) {
    move(bar, [{transform: "scaleX(0)"}, {transform: "scaleX(1)"}], 470, 90);
  }
}

function setupSequence(details) {
  const nav = details.querySelector(".sequence-controls");
  const variants = Array.from(details.querySelectorAll("[data-sequence-variant]"));
  const choices = details.querySelector("[data-variants]");
  const back = nav.querySelector("[data-frame-back]");
  const next = nav.querySelector("[data-frame-next]");
  let variant = 0;
  let frame = 0;
  let readAll = false;

  function getFrames(container) {
    return Array.from(container.querySelectorAll("[data-frame]"));
  }
  function show(withMotion = false, direction = 1) {
    variants.forEach((container, index) => {
      container.hidden = !readAll && index !== variant;
      getFrames(container).forEach((item, position) => {
        item.hidden = !readAll && position !== frame;
      });
    });
    const current = getFrames(variants[variant]);
    nav.hidden = readAll;
    if (choices) {
      choices.hidden = readAll;
      for (const button of choices.querySelectorAll("button")) {
        button.setAttribute("aria-pressed", String(Number(button.dataset.variant) === variant));
      }
    }
    details.classList.toggle("sequence-player", !readAll);
    nav.querySelector("[data-frame-count]").textContent = (frame + 1) + " / " + current.length;
    back.disabled = frame === 0;
    next.textContent = frame === current.length - 1 ? "Replay details" : "Next detail";
    if (withMotion && !readAll) animate(current[frame], direction);
  }
  choices?.addEventListener("click", event => {
    const button = event.target.closest("[data-variant]");
    if (!button) return;
    variant = Number(button.dataset.variant);
    frame = 0;
    show(true);
  });
  back.addEventListener("click", () => { frame -= 1; show(true, -1); });
  next.addEventListener("click", () => {
    frame = (frame + 1) % getFrames(variants[variant]).length;
    show(true);
  });
  details.addEventListener("toggle", () => { if (details.open) show(true); });
  show();
  return value => { readAll = value; show(); };
}

export async function setupPlayer(root) {
  const query = selector => root.querySelector(selector);
  const steps = Array.from(root.querySelectorAll("[data-step]"));
  const dialog = query("#contents");
  const counter = query("[data-counter]");
  const back = query("[data-back]");
  const next = query("[data-next]");
  const announcements = query("[data-announcement]");
  const readButton = query("[data-read-all]");
  const sequences = Array.from(root.querySelectorAll("[data-sequence]"), setupSequence);
  let current = 0;
  let readAll = false;
  const announce = text => { announcements.textContent = text; };

  function currentIndex() {
    const hash = location.hash.slice(1);
    return steps.findIndex(step => step.id === hash || (hash && step.dataset.legacy === hash));
  }
  function updateNavigation() {
    counter.textContent = "Step " + (current + 1) + " of " + steps.length;
    back.disabled = current === 0;
    next.querySelector("[data-next-label]").textContent = current === steps.length - 1 ? "Start again" : "Next";
    for (const anchor of dialog.querySelectorAll("[data-jump]")) {
      if (anchor.dataset.jump === steps[current].id) anchor.setAttribute("aria-current", "step");
      else anchor.removeAttribute("aria-current");
    }
    for (const anchor of query("[data-chapters]").querySelectorAll("[data-jump]")) {
      const first = steps.findIndex(step => step.id === anchor.dataset.jump);
      const chapter = steps[first].dataset.chapter;
      const count = steps.filter(step => step.dataset.chapter === chapter).length;
      const percent = Math.min(1, Math.max(0, (current - first + 1) / count)) * 100;
      anchor.style.setProperty("--fill", percent + "%");
      if (steps[current].dataset.chapter === chapter) anchor.setAttribute("aria-current", "step");
      else anchor.removeAttribute("aria-current");
    }
  }
  function show(index, {address = false, withMotion = false, scroll = false} = {}) {
    if (index < 0 || index >= steps.length) return;
    const previous = current;
    const positions = withMotion ? measure(steps[previous].querySelector(".scene")) : new Map();
    current = index;
    steps.forEach((step, i) => { step.hidden = !readAll && i !== current; });
    if (address && location.hash !== "#" + steps[current].id) history.pushState(null, "", "#" + steps[current].id);
    updateNavigation();
    if (withMotion && !readAll) animate(steps[current].querySelector(".scene"), current >= previous ? 1 : -1, positions);
    if (scroll || readAll && address) steps[current].scrollIntoView({block: "start", behavior: "instant"});
    announce(counter.textContent + ". " + steps[current].querySelector("h2").textContent + ".");
  }
  function navigate(index) {
    show(index, {address: true, withMotion: true, scroll: root.getBoundingClientRect().top < -30});
  }
  function onAddressChange() {
    const index = currentIndex();
    if (index >= 0 && index !== current) show(index, {scroll: readAll});
    if (!location.hash && current !== 0) show(0);
  }
  function setReadingMode(value) {
    readAll = value;
    root.classList.toggle("read-all", value);
    readButton.setAttribute("aria-pressed", String(value));
    readButton.textContent = value ? "Return to walkthrough" : "Read all steps";
    counter.closest(".transport").hidden = value;
    for (const details of root.querySelectorAll(".expander")) {
      if (value) details.open = true;
    }
    sequences.forEach(setMode => setMode(value));
    show(current, {scroll: root.getBoundingClientRect().top < -30});
  }
  function updateMotionControl() {
    for (const control of root.querySelectorAll("[data-replay]")) control.hidden = motion.matches;
    if (motion.matches) root.getAnimations({subtree: true}).forEach(animation => animation.cancel());
  }

  if (root.dataset.workflow === "evidence") {
    const {loadAtlas} = await import("./atlas.js");
    await loadAtlas(root, announce, animate);
  }
  query("[data-open-contents]").addEventListener("click", () => dialog.showModal());
  query("[data-close-contents]").addEventListener("click", () => dialog.close());
  readButton.addEventListener("click", () => setReadingMode(!readAll));
  back.addEventListener("click", () => navigate(current - 1));
  next.addEventListener("click", () => navigate((current + 1) % steps.length));
  root.addEventListener("click", event => {
    const jump = event.target.closest("[data-jump]");
    if (jump && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey) {
      event.preventDefault();
      if (dialog.open) dialog.close();
      navigate(steps.findIndex(step => step.id === jump.dataset.jump));
    }
    const replay = event.target.closest("[data-replay]");
    if (replay) animate(replay.closest("[data-step]").querySelector(".scene"));
  });
  document.addEventListener("keydown", event => {
    if (readAll || dialog.open || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.defaultPrevented ||
      event.target.closest("input, textarea, select, [contenteditable], [data-sequence], [data-choice]")) return;
    if (event.key === "ArrowRight" || event.key === "ArrowLeft") {
      event.preventDefault();
      navigate(current + (event.key === "ArrowRight" ? 1 : -1));
    }
  });
  window.addEventListener("popstate", onAddressChange);
  window.addEventListener("hashchange", onAddressChange);
  motion.addEventListener("change", updateMotionControl);
  for (const controls of root.querySelectorAll("[data-player-controls], [data-chapters]")) controls.hidden = false;
  const fromLink = currentIndex();
  show(Math.max(0, fromLink), {withMotion: true});
  updateMotionControl();
  root.dataset.enhanced = "true";
}
