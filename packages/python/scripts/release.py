import argparse
import hashlib
import json
import re
import subprocess
import time
import tomllib
import urllib.error
import urllib.request
from pathlib import Path

SOURCE_URL = "https://github.com/markhuangai/dense-mem/tree/"
PYPI_URL = "https://pypi.org/pypi/densemem"
NUMBER = r"(0|[1-9][0-9]*)"


def package_version(tag: str) -> str:
    if not re.fullmatch(rf"v{NUMBER}\.{NUMBER}\.{NUMBER}(?:-rc\.{NUMBER})?", tag):
        raise ValueError("release tag must be a canonical shared vX.Y.Z or vX.Y.Z-rc.N tag")
    return tag[1:].replace("-rc.", "rc")


def version_key(version: str) -> tuple[int, ...]:
    match = re.fullmatch(rf"{NUMBER}\.{NUMBER}\.{NUMBER}(?:rc{NUMBER})?", version)
    if not match:
        raise ValueError("unsupported published Python version")
    major, minor, patch, rc = match.groups()
    return int(major), int(minor), int(patch), int(rc is None), int(rc or 0)


def git(*args: str) -> str:
    return subprocess.check_output(["git", *args], text=True).strip()


def require_merged(source: str) -> None:
    if (
        not re.fullmatch(r"[0-9a-f]{40}", source)
        or subprocess.run(
            ["git", "merge-base", "--is-ancestor", source, "origin/main"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        ).returncode
    ):
        raise ValueError("Python release source must belong to merged main")


def fetch_json(url: str) -> dict | None:
    try:
        # PyPI JSON caches can retain partial uploads while a publication retry completes.
        with urllib.request.urlopen(
            f"{url}?release_check={time.time_ns()}", timeout=30
        ) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
        return None


def artifact_names(version: str) -> set[str]:
    version_key(version)
    return {f"densemem-{version}-py3-none-any.whl", f"densemem-{version}.tar.gz"}


def published_source(data: dict) -> str:
    url = data["info"]["project_urls"].get("Source", "")
    if not re.fullmatch(re.escape(SOURCE_URL) + r"[0-9a-f]{40}", url):
        raise ValueError("published package has no verified Dense-Mem source URL")
    return url[len(SOURCE_URL) :]


def prepare_release(tag: str, workflow_ref: str, expected_source: str = "") -> dict:
    if workflow_ref != "refs/heads/main":
        raise ValueError("publishing workflow must run from trusted main")
    version = package_version(tag)
    source = git("rev-parse", f"refs/tags/{tag}^{{commit}}")
    require_merged(source)
    if expected_source and source != expected_source:
        raise ValueError("release tag does not match the expected source")
    tree = git("rev-parse", f"{source}:packages/python")
    receipt = {
        "tag": tag,
        "version": version,
        "source_sha": source,
        "package_tree": tree,
        "should_publish": True,
        "reason": "first publication",
    }
    index = fetch_json(PYPI_URL + "/json")
    releases = index["releases"] if index is not None else {}
    if releases.get(version):
        existing = fetch_json(f"{PYPI_URL}/{version}/json")
        if existing is None or published_source(existing) != source:
            raise ValueError("existing Python version identifies a different source")
        receipt["reason"] = "verify or resume existing version"
        return receipt
    completed = []
    for previous, files in releases.items():
        if {item["filename"] for item in files if not item.get("yanked", False)} == (
            artifact_names(previous)
        ) and version_key(previous) < version_key(version):
            completed.append(previous)
    if "rc" not in version and any(item.startswith(version + "rc") for item in completed):
        receipt["reason"] = "promote Python prerelease to stable"
        return receipt
    candidates = [item for item in completed if "rc" in version or "rc" not in item]
    if candidates:
        baseline = max(candidates, key=version_key)
        previous = fetch_json(f"{PYPI_URL}/{baseline}/json")
        if previous is None:
            raise ValueError("published Python baseline disappeared")
        baseline_source = published_source(previous)
        require_merged(baseline_source)
        receipt["baseline_version"] = baseline
        receipt["should_publish"] = tree != git("rev-parse", f"{baseline_source}:packages/python")
        receipt["reason"] = "package changed" if receipt["should_publish"] else "package unchanged"
    return receipt


def stamp_version(path: Path, version: str, source: str) -> None:
    version_key(version)
    if not re.fullmatch(r"[0-9a-f]{40}", source):
        raise ValueError("invalid package source")
    text = path.read_text()
    metadata = tomllib.loads(text)
    text, count = re.subn(r'(?m)^version = "[^"]+"$', f'version = "{version}"', text)
    if count != 1:
        raise ValueError("package must have exactly one static version field")
    line = f'Source = "{SOURCE_URL}{source}"\n'
    if "Source" in metadata["project"]["urls"]:
        text = re.sub(r'(?m)^Source = "[^"]+"\n', line, text)
    else:
        text = text.replace("[project.urls]\n", "[project.urls]\n" + line)
    parsed = tomllib.loads(text)["project"]
    if parsed["version"] != version or parsed["urls"].get("Source") != SOURCE_URL + source:
        raise ValueError("failed to stamp package release metadata")
    path.write_text(text)


def verify_artifacts(receipt: dict, dist: Path, complete: bool = False) -> None:
    names = artifact_names(receipt["version"])
    local_names = {path.name for path in dist.iterdir()}
    if complete:
        local_names -= {name + ".publish.attestation" for name in names}
    if local_names != names:
        raise ValueError("release must contain exactly the selected wheel and sdist")
    data = fetch_json(f"{PYPI_URL}/{receipt['version']}/json")
    files = data["urls"] if data is not None else []
    if data is not None and published_source(data) != receipt["source_sha"]:
        raise ValueError("existing Python version identifies a different source")
    for item in files:
        name = item["filename"]
        if (
            name not in names
            or hashlib.sha256((dist / name).read_bytes()).hexdigest() != (item["digests"]["sha256"])
        ):
            raise ValueError("published artifact differs from verified release artifact")
    if complete and {item["filename"] for item in files} != names:
        raise ValueError("Python publication is incomplete")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["prepare", "stamp", "verify"])
    parser.add_argument("--tag")
    parser.add_argument("--workflow-ref")
    parser.add_argument("--source-sha", default="")
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--dist", type=Path)
    parser.add_argument("--complete", action="store_true")
    args = parser.parse_args()
    if args.command == "prepare":
        if not args.tag or not args.workflow_ref:
            parser.error("prepare requires --tag and --workflow-ref")
        receipt = prepare_release(args.tag, args.workflow_ref, args.source_sha)
        args.receipt.write_text(json.dumps(receipt, indent=2) + "\n")
        if args.output:
            with args.output.open("a") as output:
                for key in ("source_sha", "should_publish"):
                    output.write(f"{key}={str(receipt[key]).lower()}\n")
        print(receipt["reason"])
    else:
        receipt = json.loads(args.receipt.read_text())
        if args.command == "stamp":
            if git("rev-parse", "HEAD") != receipt["source_sha"]:
                raise ValueError("artifact checkout must match the selected source")
            stamp_version(
                Path("packages/python/pyproject.toml"), receipt["version"], receipt["source_sha"]
            )
        else:
            if args.dist is None:
                parser.error("verify requires --dist")
            verify_artifacts(receipt, args.dist, args.complete)


if __name__ == "__main__":
    main()
