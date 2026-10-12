import argparse
import json
import re
import subprocess
import tomllib
from pathlib import Path


def validate_release(tag: str, version: str, source: str, main: str, workflow_ref: str) -> None:
    if workflow_ref != "refs/heads/main":
        raise ValueError("publishing workflow must run from trusted main")
    if not re.fullmatch(r"python-v\d+\.\d+\.\d+(?:(?:a|b|rc)\d+)?", tag):
        raise ValueError("release tag must be python-v followed by a package version")
    if tag != "python-v" + version:
        raise ValueError("release tag does not match package metadata")
    if source != main:
        raise ValueError("release tag must identify the current merged main source")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tag", required=True)
    parser.add_argument("--workflow-ref", required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"python-v\d+\.\d+\.\d+(?:(?:a|b|rc)\d+)?", args.tag):
        raise ValueError("invalid release tag")
    source = subprocess.check_output(
        ["git", "rev-parse", f"refs/tags/{args.tag}^{{commit}}"], text=True
    ).strip()
    current = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    remote_main = subprocess.check_output(["git", "rev-parse", "origin/main"], text=True).strip()
    if current != remote_main:
        raise ValueError("checkout must match remote main")
    version = tomllib.loads(Path("packages/python/pyproject.toml").read_text())["project"][
        "version"
    ]
    validate_release(args.tag, version, source, current, args.workflow_ref)
    args.receipt.write_text(
        json.dumps({"tag": args.tag, "version": version, "source_sha": source}, indent=2) + "\n"
    )


if __name__ == "__main__":
    main()
