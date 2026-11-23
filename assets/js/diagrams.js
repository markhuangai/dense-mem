const bundle = new URL("../vendor/mermaid-12.3.0.min.js", import.meta.url);
let loading;

function library() {
  if (!loading) {
    loading = new Promise((resolve, reject) => {
      const script = document.createElement("script");
      script.src = bundle;
      script.onload = () => {
        window.mermaid.initialize({startOnLoad: false, securityLevel: "strict"});
        resolve(window.mermaid);
      };
      script.onerror = () => reject(new Error("Mermaid could not load"));
      document.head.append(script);
    }).catch(error => {
      loading = undefined;
      throw error;
    });
  }
  return loading;
}

export function setupDiagrams(blocks) {
  for (const [index, block] of Array.from(blocks).entries()) {
    const fixture = block.textContent;
    const pre = block.closest("pre") ?? block;
    const details = document.createElement("details");
    const label = document.createElement("summary");
    const view = document.createElement("div");
    const source = document.createElement("details");
    const sourceLabel = document.createElement("summary");
    const title = pre.closest("article")?.querySelector("h1")?.textContent ?? "Guide";
    details.className = "diagram-disclosure";
    label.textContent = "Show diagram";
    view.className = "diagram-render";
    sourceLabel.textContent = "Diagram source";
    pre.before(details);
    details.append(label, view, source);
    source.append(sourceLabel, pre);
    details.addEventListener("toggle", async () => {
      if (!details.open || details.dataset.rendering) return;
      details.dataset.rendering = "true";
      try {
        const mermaid = await library();
        const {svg} = await mermaid.render("wiki-diagram-" + index, fixture);
        view.innerHTML = svg;
        const rendered = view.querySelector("svg");
        if (!rendered.hasAttribute("aria-labelledby")) rendered.setAttribute("aria-label", title + " diagram " + (index + 1));
      } catch (error) {
        delete details.dataset.rendering;
        view.textContent = "The diagram could not render. Its source is available below.";
        source.open = true;
        console.error(error);
      }
    });
  }
}
