import argparse
import collections
import hashlib
import json
from pathlib import Path


FAMILIES = (
    "request_id_mismatch", "item_locked_classification",
    "item_ambiguous_classification", "item_reason_limit",
    "commit_conflict", "classification_ambiguous",
)


def source_key(source):
    return source["kind"], source["id"], source["version"]


def select_witnesses(assessments):
    ordered = sorted(assessments, key=lambda row: (
        len(row["body"]["sources"]), row["created_at"], row["assessment_id"],
    ))
    selected = []
    for family in FAMILIES:
        for row in ordered:
            result = row["body"]["result"]
            codes = {attempt.get("validation", {}).get("code")
                     for attempt in result.get("attempts", [])}
            codes.add(result.get("failure_code"))
            codes.update(outcome.get("reason") for outcome in result["outcomes"])
            if family in codes:
                selected.append({
                    "family": family,
                    "original_assessment_id": row["assessment_id"],
                    "original_created_at": row["created_at"],
                    "historical_context_available": False,
                    "original_receipt": row["body"],
                })
                break
        else:
            raise ValueError(f"no retained assessment for {family}")
    return selected


def reconcile(assessments, terminal_sources):
    by_id = {}
    for row in assessments:
        identifier = row["assessment_id"]
        if identifier in by_id or row["body"]["id"] != identifier:
            raise ValueError("duplicate or mismatched assessment identity")
        by_id[identifier] = row["body"]
    terminal = {}
    unresolved = []
    invalid_codes = collections.Counter()
    for source in terminal_sources:
        key = source_key(source)
        if key in terminal:
            raise ValueError("duplicate terminal source identity")
        terminal[key] = source
        if source["status"] not in ("failed", "ambiguous"):
            continue
        receipt = by_id.get(source["assessment_id"])
        if receipt is None:
            raise ValueError("terminal source has no retained assessment")
        dependencies = [item for item in receipt["sources"] if source_key(item) == key]
        outcomes = [item for item in receipt["result"]["outcomes"] if source_key(item["source"]) == key]
        if len(dependencies) != 1 or dependencies[0]["fingerprint"] != source["fingerprint"]:
            raise ValueError("terminal source fingerprint differs from assessment")
        if len(outcomes) != 1 or (outcomes[0]["status"], outcomes[0].get("reason", "")) != (
                source["status"], source["reason"]):
            raise ValueError("terminal source outcome differs from assessment")
        unresolved.append(source)
        if source["reason"] == "provider_response_invalid":
            invalid_codes[receipt["result"]["attempts"][-1]["validation"]["code"]] += 1
    appearances = []
    for receipt in by_id.values():
        if receipt["result"].get("failure_code") == "provider_response_invalid":
            appearances.extend((*source_key(source), source["fingerprint"]) for source in receipt["sources"])
    dispositions = collections.Counter()
    for kind, identifier, version, fingerprint in set(appearances):
        current = terminal.get((kind, identifier, version))
        if current is None or current["fingerprint"] != fingerprint:
            raise ValueError("historical invalid appearance lacks an exact terminal source")
        dispositions[f'{current["status"]}:{current["reason"]}'] += 1
    return {
        "eligible_sources": len(terminal),
        "unresolved_sources": len(unresolved),
        "terminal_outcomes": dict(collections.Counter(
            f'{source["status"]}:{source["reason"]}' for source in terminal.values())),
        "terminal_invalid_validation_codes": dict(invalid_codes),
        "invalid_assessment_source_appearances": len(appearances),
        "invalid_assessment_unique_sources": len(set(appearances)),
        "invalid_assessment_repeated_appearances": len(appearances) - len(set(appearances)),
        "current_dispositions_of_invalid_sources": dict(dispositions),
        "source_lineage": sorted(unresolved, key=source_key),
    }


def main():
    parser = argparse.ArgumentParser(description="Reconcile retained ontology outcomes and freeze diagnostic witnesses.")
    parser.add_argument("--assessments", type=Path, required=True)
    parser.add_argument("--terminal-sources", type=Path, required=True)
    parser.add_argument("--expected-assessments-sha256", required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    raw = args.assessments.read_bytes()
    digest = hashlib.sha256(raw).hexdigest()
    if digest != args.expected_assessments_sha256.removeprefix("sha256:"):
        raise ValueError("retained assessment export hash differs")
    assessments = [json.loads(line) for line in raw.splitlines() if line.strip()]
    terminal_bytes = args.terminal_sources.read_bytes()
    terminal = json.loads(terminal_bytes)
    result = {
        "schema_version": "dense-mem.ontology.coverage_diagnostics.v1",
        "assessments_sha256": digest,
        "terminal_sources_sha256": hashlib.sha256(terminal_bytes).hexdigest(),
        "scope": terminal["scope"],
        "reconciliation": reconcile(assessments, terminal["sources"]),
        "witnesses": select_witnesses(assessments),
        "historical_provider_bodies_available": False,
    }
    with args.out.open("x", encoding="utf-8") as output:
        args.out.chmod(0o600)
        json.dump(result, output, indent=2)
        output.write("\n")
    print(json.dumps({"unresolved_sources": result["reconciliation"]["unresolved_sources"],
                      "witnesses": len(result["witnesses"]), "assessments_sha256": digest}))


if __name__ == "__main__":
    main()
