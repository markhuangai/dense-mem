"""Check generated pages with GitHub Pages' mounted project path."""
from collections import Counter
from html.parser import HTMLParser
import json
from pathlib import Path
import re
import subprocess
import sys
from urllib.error import URLError
from urllib.parse import quote, unquote, urljoin, urlsplit
from urllib.request import urlopen

BASE = "https://markhuangai.github.io/dense-mem/"
REPOSITORY = "https://github.com/markhuangai/dense-mem/"
ROOT = Path(sys.argv[1]).resolve()
errors = []
checked_blobs = {}
counts = Counter()


class Page(HTMLParser):
    def __init__(self, filename):
        super().__init__(convert_charrefs=True)
        self.anchors = Counter()
        self.links = []
        self.diagrams = 0
        self.steps = []
        self.lesson = None
        self.coverage = []
        self.feed(filename.read_text())

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if "id" in attrs:
            self.anchors[attrs["id"]] += 1
        for field in ("href", "src", "action", "data-index"):
            if field in attrs:
                self.links.append(attrs[field])
        if "language-mermaid" in attrs.get("class", "").split():
            self.diagrams += 1
        if tag == "main" and "data-lesson" in attrs:
            self.lesson = attrs["data-lesson"]
            self.coverage = attrs["data-covers"].split()
        if tag == "article" and "data-step" in attrs:
            self.steps.append(attrs["id"])


def check_source(relative, text):
    counts["repository links"] += 1
    uri = urlsplit(relative)
    parts = unquote(uri.path).split("/", 2)
    if len(parts) != 3 or parts[0] != "blob" or not re.fullmatch(r"[a-f0-9]{40}", parts[1]):
        return
    ref = parts[1] + ":" + parts[2]
    if ref not in checked_blobs:
        result = subprocess.run(["git", "show", ref], capture_output=True, check=False)
        if result.returncode == 0:
            checked_blobs[ref] = len(result.stdout.splitlines())
        else:
            # GitHub retains some cited review commits outside branch history.
            raw = "https://raw.githubusercontent.com/markhuangai/dense-mem/" + parts[1] + "/" + quote(parts[2])
            try:
                with urlopen(raw, timeout=10) as response:
                    checked_blobs[ref] = len(response.read().splitlines())
            except (URLError, OSError) as failure:
                errors.append(f"{text}: unable to read pinned source {ref} ({failure})")
                checked_blobs[ref] = None
    length = checked_blobs[ref]
    for line in re.findall(r"L(\d+)", uri.fragment):
        if length is not None and not 1 <= int(line) <= length:
            errors.append(f"{text}: line {line} outside {ref}")


def resolve(path):
    absolute = ROOT / unquote(path)
    if absolute.is_dir():
        absolute /= "index.html"
    return absolute


def check_link(url, context):
    if url.startswith(REPOSITORY):
        if url == REPOSITORY + "wiki" or url.startswith(REPOSITORY + "wiki/"):
            errors.append(f"{context}: link still points to Wiki: {url}")
        check_source(url[len(REPOSITORY):], context)
        return
    target = urlsplit(urljoin(BASE + context, url))
    if target.scheme not in ("http", "https") or target.netloc != "markhuangai.github.io":
        return
    if not target.path.startswith("/dense-mem/"):
        errors.append(f"{context}: project path is missing in {url}")
        return
    filename = resolve(target.path.removeprefix("/dense-mem/"))
    if not filename.is_file():
        errors.append(f"{context}: missing path {url}")
    elif target.fragment and filename in pages:
        if unquote(target.fragment) not in pages[filename].anchors:
            errors.append(f"{context}: missing anchor {url}")
    counts["site links"] += 1


def verify_migration():
    inventory = json.loads(Path("_data/migration.json").read_text())
    legacy_files = subprocess.check_output(
        ["git", "ls-tree", "--name-only", inventory["revision"]], text=True
    ).splitlines()
    expected = {name for name in legacy_files if name.endswith(".md")}
    if expected != set(inventory["files"]):
        errors.append("The migration inventory does not cover the source Markdown tree")
    for name, record in inventory["files"].items():
        filename = resolve(record["path"].lstrip("/"))
        page = pages.get(filename)
        if not page:
            errors.append(f"{name}: missing migrated page {record['path']}")
            continue
        for anchor in record["anchors"]:
            if anchor not in page.anchors:
                errors.append(f"{name}: missing original heading {anchor}")
        if page.diagrams < record["diagrams"]:
            errors.append(f"{name}: expected {record['diagrams']} diagrams, found {page.diagrams}")
    counts["legacy files"] = len(expected)


def verify_lessons():
    contracts = json.loads(Path("_data/contracts.json").read_text())
    lesson_pages = [page for page in pages.values() if page.lesson]
    coverage = {item for page in lesson_pages for item in page.coverage}
    # Supported surfaces checked against the tool registry, portal navigation, and runtime composition.
    required = set(contracts["tools"]) | set("""
        ui:sign-in ui:workspace-picker ui:connect ui:search ui:team ui:my-credentials ui:ontology
        control:all-teams control:overview control:credentials control:remember-attempts
        control:evidence-conflicts control:settings control:search control:recall-feedback control:logs
        control:maintenance control:sso control:security control:config
        control-config:general control-config:sso control-config:operation-logs
        control-config:recall-feedback control-config:telemetry-pricing control-config:decisions
        control-sso:providers control-sso:mappings control-sso:directory
        worker:operation-log worker:credential-activity worker:usage-metrics worker:recall-feedback
        worker:private-memory worker:private-retention worker:telemetry-pricing-refresh
        worker:search-reconciliation worker:remember-diagnostics worker:ontology-maintenance worker:demo-cleanup
        ops:deploy ops:migrations ops:backup ops:restore ops:monitoring ops:evaluation
        profile:production profile:demo profile:evaluation
    """.split())
    for item in sorted(required - coverage):
        errors.append(f"Missing lesson coverage for {item}")
    catalog = pages[resolve("system/")]
    for page in lesson_pages:
        route = f"/dense-mem/learn/{page.lesson}/"
        if route not in catalog.links:
            errors.append(f"Lesson is missing from the system index: {route}")
    home = pages[resolve("")]
    for number, step_id in enumerate(pages[resolve("walkthroughs/atlas/")].steps, start=1):
        for alias in (step_id, f"step-{number}"):
            if alias not in home.anchors:
                errors.append(f"Missing homepage Atlas bookmark: {alias}")
    counts["lessons"] = len(lesson_pages)
    counts["documented surfaces"] = len(required)


pages = {file: Page(file) for file in ROOT.rglob("*.html")}
for file, page in pages.items():
    path = file.relative_to(ROOT).as_posix()
    if path.endswith("index.html"):
        path = path.removesuffix("index.html")
    for link in page.links:
        check_link(link, path)
    for anchor, count in page.anchors.items():
        if count > 1:
            errors.append(f"{path}: repeated ID {anchor}")
    counts["diagrams"] += page.diagrams
    counts["steps"] += len(page.steps)
    counts["walkthroughs"] += bool(page.steps)

verify_migration()
verify_lessons()
search = json.loads((ROOT / "assets/search.json").read_text())
for entry in [*search["guides"], *search["steps"], *search["lessons"], *search["fields"]]:
    check_link(entry["url"], "search/")

if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print(f"Verified {len(pages)} HTML pages, " + ", ".join(f"{n} {name}" for name, n in counts.items()))
