function followBookmark() {
  const bookmark = document.getElementById(location.hash.slice(1))?.querySelector("[data-bookmark]");
  if (bookmark) location.replace(bookmark.href);
}
if (document.querySelector("[data-bookmark]")) {
  followBookmark();
  window.addEventListener("hashchange", followBookmark);
}

const entrances = document.querySelectorAll(".learn-home [data-enter]");
if (entrances.length && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
  const enter = new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue;
      entry.target.classList.add("is-in-view");
      enter.unobserve(entry.target);
    }
  }, {threshold: 0.18});
  for (const element of entrances) enter.observe(element);
}

const lesson = document.querySelector("[data-lesson]");
if (lesson) import("./lesson.js").then(module => module.setupLesson(lesson));

const player = document.querySelector("[data-player]");
if (player) {
  import("./player.js").then(module => module.setupPlayer(player)).catch(error => {
    console.error(error);
    const notice = document.createElement("p");
    notice.textContent = "The animated player could not start. All steps are available below.";
    player.querySelector(".intro").after(notice);
  });
}

const search = document.querySelector("[data-search]");
if (search) import("./search.js").then(module => module.setupSearch(search));

for (const term of document.querySelectorAll("[data-term]")) {
  const button = term.querySelector("button");
  const reveal = () => term.removeAttribute("data-dismissed");
  term.addEventListener("pointerenter", reveal);
  term.addEventListener("focusin", reveal);
  button.addEventListener("click", () => {
    const expanded = button.getAttribute("aria-expanded") === "true";
    button.setAttribute("aria-expanded", String(!expanded));
    term.toggleAttribute("data-dismissed", expanded);
  });
  term.addEventListener("keydown", event => {
    if (event.key === "Escape") {
      button.setAttribute("aria-expanded", "false");
      term.setAttribute("data-dismissed", "");
    }
  });
}

const diagramBlocks = document.querySelectorAll(".language-mermaid");
if (diagramBlocks.length > 0) import("./diagrams.js").then(module => module.setupDiagrams(diagramBlocks));
