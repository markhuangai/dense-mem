"""Materialize the pinned disposable ontology study checkout."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess

BASE = "214fee2f8d3ac53333857888dcdd2ce56a367fa9"
FIXTURES = "57e6cf418ed4c5ea5413ae0c708e7a72f29ff7d4"
ADAPTER = Path("internal/ontology/postgres")
FIXTURE_PATHS = (
    *(ADAPTER / name for name in (
        "fixture_test.go", "organization_fixture_test.go", "build_study_fixture_test.go",
        "build_study_integration_test.go", "build_study_vector_test.go", "build_study_rank_test.go"
    )),
    Path("tests/eval/scripts/ontology_build_vectors.sql"),
    Path("tests/eval/scripts/ontology_build_study.py"),
)


def git(source, *args):
    return subprocess.check_output(["git", "-C", source, *args])


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def materialize(destination, mode, source):
    if not destination.is_absolute():
        raise ValueError("use an absolute disposable path")
    directory = source / "tests/eval/ontology-receipts"
    destination.mkdir(mode=0o700)
    archive = git(source, "archive", BASE)
    subprocess.run(["tar", "-x", "-C", destination], input=archive, check=True)
    runtime = directory / "runtime.patch"
    subprocess.run(["git", "apply", "--unidiff-zero", "--check", runtime], cwd=destination, check=True)
    subprocess.run(["git", "apply", "--unidiff-zero", runtime], cwd=destination, check=True)
    migration = directory / "receipt_recheck.sql"

    if mode == "hosted":
        for path in FIXTURE_PATHS:
            (destination / path).write_bytes(git(source, "show", f"{FIXTURES}:{path}"))
        fixture = destination / ADAPTER / "build_study_integration_test.go"
        patch = directory / "study_fixture.patch"
        subprocess.run(["git", "apply", "--unidiff-zero", "--check", patch], cwd=destination, check=True)
        subprocess.run(["git", "apply", "--unidiff-zero", patch], cwd=destination, check=True)
        shutil.copyfile(directory / "receipt_recheck_test.go.src", destination / ADAPTER / "receipt_recheck_study_integration_test.go")
        vector = subprocess.check_output(
            ["python3", "tests/eval/scripts/ontology_build_study.py", "render-vector", str(destination / "study-vectors.sql")],
            cwd=destination,
        )
        if vector:
            print(vector.decode())
    else:
        shutil.copyfile(migration, destination / "migrations/postgres/v2_8/20991123010000_receipt_recheck_study.sql")
        shutil.copyfile(directory / "trial.mjs", destination / "tests/eval/scripts/receipt-trial.mjs")
    receipt = {
        "base_source": BASE,
        "fixture_source": FIXTURES,
        "study_source": git(source, "rev-parse", "HEAD").decode().strip(),
        "runtime_patch_sha256": digest(runtime),
        "migration_sha256": digest(migration),
        "mode": mode,
    }
    (destination / "ontology-receipt-study-identity.json").write_text(json.dumps(receipt, indent=2) + "\n")
    return receipt


def github_setup(source, destination, receipt):
    directory = Path(os.environ["RUNNER_TEMP"]) / "ontology-receipt-results"
    directory.mkdir(mode=0o700)
    (directory / "identity.json").write_text(json.dumps(receipt) + "\n")
    values = {
        "STUDY_TREE": destination,
        "ONTOLOGY_STUDY_REPORT_DIR": directory,
        "ONTOLOGY_STUDY_RECEIPT_SQL": source / "tests/eval/ontology-receipts/receipt_recheck.sql",
        "ONTOLOGY_STUDY_VECTOR_SQL": destination / "study-vectors.sql",
    }
    with Path(os.environ["GITHUB_ENV"]).open("a") as output:
        for key, value in values.items():
            output.write(f"{key}={value}\n")


def main():
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("mode", choices=("hosted", "private"))
    cli.add_argument("destination", type=Path)
    args = cli.parse_args()
    source = Path(__file__).resolve().parents[3]
    if os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise SystemExit("the ontology study requires GitHub-hosted PostgreSQL")
    hosted = args.mode == "hosted" and os.environ.get("GITHUB_ACTIONS") == "true"
    if hosted:
        for revision in (BASE, FIXTURES):
            known = subprocess.run(["git", "-C", source, "cat-file", "-e", revision + "^{commit}"], capture_output=True)
            if known.returncode:
                subprocess.run(["git", "-C", source, "fetch", "--depth=1", "--no-tags", "origin", revision], check=True)
    receipt = materialize(args.destination, args.mode, source)
    if hosted:
        github_setup(source, args.destination, receipt)
    print(json.dumps(receipt))


if __name__ == "__main__":
    main()
