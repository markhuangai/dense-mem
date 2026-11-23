const normalize = text => text.normalize("NFKC").trim().toLocaleLowerCase("en").replace(/\s+/g, " ");

function guideSections(guide) {
  const html = new DOMParser().parseFromString(guide.html, "text/html");
  const entries = [{title: guide.title, url: guide.url, context: guide.title, text: ""}];
  for (const element of html.body.children) {
    if (/^H[2-6]$/.test(element.tagName) && element.id) {
      entries.push({
        title: element.textContent, url: guide.url + "#" + encodeURIComponent(element.id),
        context: guide.title, text: ""
      });
    } else if (element.tagName !== "H1") {
      entries.at(-1).text += " " + element.textContent;
    }
  }
  return entries;
}

function searchEntries(index, query) {
  const normalized = normalize(query);
  const terms = normalized.split(" ");
  return index.map(entry => {
    const title = normalize(entry.title);
    const text = normalize(entry.text);
    const body = title + " " + normalize(entry.context) + " " + text;
    if (terms.some(term => !body.includes(term))) return null;
    const score = (title === normalized ? 100 : 0) + (title.includes(normalized) ? 40 : 0) +
      (text.includes(normalized) ? 12 : 0) + terms.filter(term => title.includes(term)).length * 5;
    return {...entry, score};
  }).filter(Boolean).sort((a,b) => b.score - a.score || a.title.localeCompare(b.title)).slice(0, 30);
}

function excerpt(text, query) {
  const cleaned = text.trim().replace(/\s+/g, " ");
  const match = cleaned.toLocaleLowerCase("en").indexOf(normalize(query));
  const start = Math.max(0, match - 44);
  const snippet = cleaned.slice(start, start + 174).trim();
  return (start > 0 ? "…" : "") + snippet + (start + 174 < cleaned.length ? "…" : "");
}

export function setupSearch(root) {
  const form = root.querySelector("form");
  const input = root.querySelector("input");
  const results = root.querySelector("[data-search-results]");
  const status = root.querySelector("[data-search-status]");
  let loading;
  let sequence = 0;
  async function load() {
    const response = await fetch(root.dataset.index);
    if (!response.ok) throw new Error("Search index returned " + response.status);
    const index = await response.json();
    return [
      ...index.guides.flatMap(guideSections),
      ...index.steps.map(step => ({
        title: step.title, url: step.url, context: step.guide,
        text: new DOMParser().parseFromString(step.html, "text/html").body.textContent + " " + step.detail
      }))
    ];
  }
  async function render(value) {
    const token = ++sequence;
    results.replaceChildren();
    if (!normalize(value)) { status.textContent = ""; return; }
    status.textContent = "Searching…";
    try {
      loading ??= load();
      const index = await loading;
      if (token !== sequence) return;
      const found = searchEntries(index, value);
      status.textContent = found.length === 0 ? "No results found." : "Showing " + found.length + (found.length === 1 ? " result." : " results.");
      for (const item of found) {
        const row = document.createElement("li");
        const link = document.createElement("a");
        link.href = item.url;
        link.textContent = item.title;
        const context = document.createElement("div");
        context.className = "paper-sub";
        context.textContent = item.context;
        const text = document.createElement("p");
        text.textContent = excerpt(item.text, value);
        row.append(link, context, text);
        results.append(row);
      }
    } catch (error) {
      console.error(error);
      loading = undefined;
      if (token === sequence) status.textContent = "Search is unavailable. You can browse all guides.";
    }
  }
  function readAddress() {
    input.value = new URLSearchParams(location.search).get("q") ?? "";
    render(input.value);
  }
  form.addEventListener("submit", event => {
    event.preventDefault();
    const url = new URL(location.href);
    url.searchParams.set("q", input.value.trim());
    history.pushState(null, "", url);
    render(input.value);
  });
  window.addEventListener("popstate", readAddress);
  readAddress();
}
