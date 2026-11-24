"""Export or check the MCP field reference against the pinned Go registry."""
import argparse
import difflib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import subprocess
from tempfile import TemporaryDirectory

SITE = Path(__file__).resolve().parents[1]
EXPORT = r'''
package registry

import (
    "encoding/json"
    "os"
    "testing"
)

func TestDocumentationContractExport(t *testing.T) {
    schemas := map[string]map[string]any{}
    for _, tool := range ContractTools() {
        schemas[tool.Name] = map[string]any{
            "input": tool.InputSchema,
            "output": tool.OutputSchema,
        }
    }
    data, err := json.Marshal(map[string]any{"tools": schemas, "error": PublicErrorSchema()})
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(os.Getenv("DENSE_MEM_CONTRACT_EXPORT"), data, 0600); err != nil { t.Fatal(err) }
}

func TestDocumentationRequests(t *testing.T) {
    file := os.Getenv("DENSE_MEM_DOCUMENTATION_REQUESTS")
    if file == "" { t.Skip("no rendered documentation supplied") }
    raw, err := os.ReadFile(file)
    if err != nil { t.Fatal(err) }
    var requests []struct {
        Tool string `json:"tool"`
        Arguments map[string]any `json:"arguments"`
    }
    if err := json.Unmarshal(raw, &requests); err != nil { t.Fatal(err) }
    tools := map[string]Tool{}
    for _, tool := range ContractTools() { tools[tool.Name] = tool }
    for _, request := range requests {
        tool, ok := tools[request.Tool]
        if !ok { t.Fatalf("unknown tool %q", request.Tool) }
        if err := ValidateContractInput(tool, request.Arguments, []string{"read", "write"}); err != nil {
            t.Errorf("%s example: %v", request.Tool, err)
        }
    }
}
'''


def fields(schema, prefix=""):
    required = set(schema.get("required", []))
    for key, prop in schema.get("properties", {}).items():
        path = prefix + key
        typ = prop.get("type")
        if isinstance(typ, list):
            typ = " or ".join(typ)
        typ = prop.get("format", typ) or ("enum" if "enum" in prop else "variant")
        constraints = {name: prop[name] for name in (
            "enum", "default", "minimum", "maximum", "minLength", "maxLength",
            "minItems", "maxItems", "maxProperties", "pattern", "uniqueItems",
            "x-max-depth", "x-max-bytes",
        ) if name in prop}
        yield dict(path=path, type=typ, required=key in required,
                   purpose=prop.get("description", ""), constraints=constraints)
        yield from fields(prop, path + ".")
        if isinstance(prop.get("items"), dict):
            yield from fields(prop["items"], path + "[].")
        for variant in prop.get("oneOf", []):
            yield from fields(variant, path + ".")


class Requests(HTMLParser):
    def __init__(self, rendered):
        super().__init__()
        self.examples = []
        self.tool = None
        self.body = []
        self.feed(rendered)

    def handle_starttag(self, tag, attrs):
        if tag == "code":
            self.tool = dict(attrs).get("data-tool-request")
            self.body = []

    def handle_data(self, data):
        if self.tool:
            self.body.append(data)

    def handle_endtag(self, tag):
        if tag == "code" and self.tool:
            self.examples.append({"tool": self.tool, "arguments": json.loads("".join(self.body))})
            self.tool = None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("application", type=Path, help="Checkout at the pinned lesson source revision")
    parser.add_argument("--preview", type=Path, help="Also validate MCP requests from this rendered site")
    parser.add_argument("--write", action="store_true", help="Update the checked-in field reference")
    args = parser.parse_args()
    revision = re.search(r"^lesson_source_revision: ([0-9a-f]{40})$", (SITE / "_config.yml").read_text(), re.M)[1]
    app = args.application.resolve()
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=app, text=True).strip()
    if head != revision:
        parser.error(f"expected application revision {revision}, found {head}")
    export = SITE / "_data/contracts.json"
    with TemporaryDirectory(prefix="dense-mem-contracts-") as tmp:
        tmp = Path(tmp)
        (tmp / "export_test.go").write_text(EXPORT)
        (tmp / "overlay.json").write_text(json.dumps({"Replace": {
            str(app / "internal/tools/registry/docs_export_test.go"): str(tmp / "export_test.go"),
        }}))
        env = {**os.environ, "DENSE_MEM_CONTRACT_EXPORT": str(tmp / "schemas.json")}
        if args.preview:
            requests = [example for page in (args.preview / "learn").rglob("*.html")
                        for example in Requests(page.read_text()).examples]
            if not {"remember", "recall_memory", "trace_memory"}.issubset(example["tool"] for example in requests):
                raise SystemExit("Core story MCP requests are missing from the build")
            (tmp / "requests.json").write_text(json.dumps(requests))
            env["DENSE_MEM_DOCUMENTATION_REQUESTS"] = str(tmp / "requests.json")
        subprocess.run(["go", "test", "-count=1", "-overlay", str(tmp / "overlay.json"),
                        "-run", "^TestDocumentation(ContractExport|Requests)$", "./internal/tools/registry"],
                       cwd=app, env=env, check=True)
        api = json.loads((tmp / "schemas.json").read_text())
    snapshot = {
        "revision": revision,
        "tools": {name: {"request": list(fields(tool["input"])), "result": list(fields(tool["output"]["oneOf"][0]))}
                  for name, tool in api["tools"].items()},
        "error": list(fields(api["error"])),
    }
    generated = json.dumps(snapshot, indent=2, ensure_ascii=False) + "\n"
    if args.write:
        export.write_text(generated)
    elif export.read_text() != generated:
        print("".join(difflib.unified_diff(export.read_text().splitlines(keepends=True), generated.splitlines(keepends=True),
                                         str(export), "registry export")), end="")
        raise SystemExit("The checked-in MCP field reference differs from the pinned registry")


if __name__ == "__main__":
    main()
