#!/usr/bin/env python3
"""Check frozen session-quality and paired Remember evidence for issue #218."""
import argparse
import json
from pathlib import Path


def compare(quality, base, candidate):
    if quality["mode"] != "quality" or len(quality["repetitions"]) != 3 or not quality["passed"]:
        raise ValueError("three complete real-provider quality repetitions are required")
    if base["mode"] != "remember" or candidate["mode"] != "remember" or not base["passed"] or not candidate["passed"]:
        raise ValueError("both Remember controls must pass")
    if len({item["cohort_sha256"] for item in (quality, base, candidate)}) != 1:
        raise ValueError("comparison must use one frozen cohort")
    for repetition in quality["repetitions"]:
        if len(repetition["samples"]) != 32 or repetition["precision"] < .98 or repetition["recall"] < .95:
            raise ValueError("frozen quality threshold failed")
    before = base["repetitions"][0]
    after = candidate["repetitions"][0]
    if len(before["samples"]) != 12 or len(after["samples"]) != 12:
        raise ValueError("twelve paired Remember controls are required")
    controls = []
    for first, second in zip(before["samples"], after["samples"], strict=True):
        if first["id"] != second["id"] or sorted(first["facts"], key=lambda fact: json.dumps(fact, sort_keys=True)) != sorted(second["facts"], key=lambda fact: json.dumps(fact, sort_keys=True)) or first["outcome"] != second["outcome"] or first["source"] != second["source"]:
            raise ValueError("Remember expected facts changed")
        controls.append({"id": first["id"], "source": first["source"], "outcome": first["outcome"], "base_seconds": first["processing_seconds"],
                         "candidate_seconds": second["processing_seconds"],
                         "base_usage": first["usage"], "candidate_usage": second["usage"]})
    if quality["source_sha"] != candidate["source_sha"]:
        raise ValueError("quality and candidate controls must certify the same commit")
    return {"schema_version": "dense-mem.session_ingest.validation.v1", "issue": 218,
            "cohort_sha256": quality["cohort_sha256"], "base_sha": base["source_sha"],
            "candidate_sha": candidate["source_sha"], "quality": [{key: value for key, value in item.items() if key != "samples"} for item in quality["repetitions"]],
            "token_accounting": quality["token_accounting"],
            "remember_controls": controls, "verified": True}


def main():
    parser = argparse.ArgumentParser()
    for name in ("quality", "base", "candidate", "out"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    report = compare(*(json.loads(getattr(args, name).read_text()) for name in ("quality", "base", "candidate")))
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"verified": True, "candidate_sha": report["candidate_sha"]}))


if __name__ == "__main__":
    main()
