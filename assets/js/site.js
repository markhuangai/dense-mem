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
