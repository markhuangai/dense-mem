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
    let requestedTheme;
    let renderVersion = 0;
    details.className = "diagram-disclosure";
    label.textContent = "Show diagram";
    view.className = "diagram-render";
    sourceLabel.textContent = "Diagram source";
    pre.before(details);
    details.append(label, view, source);
    source.append(sourceLabel, pre);
    async function render() {
      const theme = document.documentElement.dataset.theme === "dark" ? "dark" : "default";
      if (!details.open || requestedTheme === theme) return;
      requestedTheme = theme;
      const version = ++renderVersion;
      try {
        const mermaid = await library();
        if (version !== renderVersion) return;
        const themedFixture = `${fixture}\n%%{init: {"theme": "${theme}"}}%%`;
        const {svg} = await mermaid.render(`wiki-diagram-${index}-${version}`, themedFixture);
        if (version !== renderVersion) return;
        view.innerHTML = svg;
        const rendered = view.querySelector("svg");
        if (!rendered.hasAttribute("aria-labelledby")) rendered.setAttribute("aria-label", title + " diagram " + (index + 1));
      } catch (error) {
        if (version !== renderVersion) return;
        requestedTheme = undefined;
        view.textContent = "The diagram could not render. Its source is available below.";
        source.open = true;
        console.error(error);
      }
    }
    details.addEventListener("toggle", render);
    document.addEventListener("docs-theme-change", render);
  }
}
