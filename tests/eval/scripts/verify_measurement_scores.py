#!/usr/bin/env python3
"""Verify Go scoring against hand examples, pinned IR measures and retained runs."""

import argparse
import hashlib
import importlib.metadata
import json
import math
from pathlib import Path

import ir_measures
from ir_measures import R, RR, nDCG


METRICS = ("recall_at_k", "mrr", "ndcg_at_k")


def file_hash(path):
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


def check_close(expected, observed, context):
    if not math.isfinite(observed) or not math.isclose(expected, observed, abs_tol=1e-12, rel_tol=1e-12):
        raise ValueError(f"score discrepancy for {context}: expected {expected}, observed {observed}")


def reference_examples(log):
    marker = "measurement_reference_scores="
    matches = [json.loads(line.split(marker, 1)[1]) for line in log.read_text().splitlines() if marker in line]
    if len(matches) != 1:
        raise ValueError("require exactly one real Go reference-test output")
    fixture = Path(__file__).parents[1] / "fixtures/measurement_score_cases.json"
    examples = json.loads(fixture.read_text())
    observed = matches[0]
    if set(observed) != {example["id"] for example in examples}:
        raise ValueError("Go reference cases are missing, duplicated or unexpected")
    for example in examples:
        for metric in METRICS:
            check_close(example["expected"][metric], observed[example["id"]][metric], example["id"] + "/" + metric)
        if not example["standard"]:
            continue
        # Dense-Mem uses exponential gains; pytrec_eval consumes the supplied gains directly.
        judgments = [ir_measures.Qrel(example["id"], ref["id"], int(2 ** ref["grade"] - 1))
                     for ref in example["qrel"]["required_refs"]]
        ranking = [ir_measures.ScoredDoc(example["id"], ref["id"], -rank)
                   for rank, ref in enumerate(example["trace"]["ranked_refs"], 1)]
        measures = dict(zip(METRICS, [R @ example["k"], RR @ example["k"], nDCG @ example["k"]]))
        if ranking:
            values = ir_measures.calc_aggregate(list(measures.values()), judgments, ranking)
            for metric, measure in measures.items():
                check_close(values[measure], observed[example["id"]][metric], example["id"] + "/ir-measures/" + metric)
        elif any(observed[example["id"]][metric] != 0 for metric in METRICS):
            raise ValueError("explicitly empty ranking must score zero")
    return {"cases": len(examples), "ir_measures_cases": sum(example["standard"] and bool(example["trace"]["ranked_refs"]) for example in examples),
            "fixture_sha256": file_hash(fixture), "go_log_sha256": file_hash(log),
            "group_semantics": "Hand-checked source coverage and one displayed result per slot; grouped and repeated slots are not treated as ordinary TREC documents."}


def rows(path):
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def verify_aggregates(scores, summary, label, rank_rates):
    result = {}
    if summary["case_count"] != len(scores):
        raise ValueError("case denominator mismatch: " + label)
    for prefix in ("", "context_", "evidence_", "dream_"):
        selected = scores if not prefix else [row for row in scores if row.get(prefix + "scored", False)]
        if prefix and summary[prefix + "scored_case_count"] != len(selected):
            raise ValueError("scored denominator mismatch: " + label + "/" + prefix)
        for metric in (*METRICS, "bad_at_k"):
            value = math.fsum(row[prefix + metric] for row in selected) / len(selected) if selected else 0
            name = "average_" + prefix + metric
            check_close(value, summary[name], label + "/" + name)
            result[name] = value
        if rank_rates:
            for kind in ("required", "bad"):
                value = sum(row.get(prefix + "first_" + kind + "_rank", 0) == 1 for row in selected) / len(selected) if selected else 0
                name = prefix + kind + "_rank1_rate"
                check_close(value, summary[name], label + "/" + name)
                result[name] = value
    if rank_rates:
        if summary["scored_case_count"] != len(scores):
            raise ValueError("scored case denominator mismatch: " + label)
        if summary["unmapped_source_refs"] != sum(len(row.get("unmapped_source_refs", [])) for row in scores):
            raise ValueError("unmapped source count mismatch: " + label)
    return result


def frozen_cases(path):
    manifest = json.loads(path.read_text())
    files = [path, path.parent / manifest["corpus_file"], path.parent / manifest["cases_file"], path.parent / manifest["qrels_file"]]
    files.extend(path.parent / manifest[field] for field in ("answers_file", "hard_negatives_file", "transforms_file", "dreams_file", "licenses_file") if manifest.get(field))
    digest = hashlib.sha256()
    for item in files:
        digest.update(("file:" + item.name + "\n").encode())
        digest.update(item.read_bytes())
        digest.update(b"\n")
    cases = rows(path.parent / manifest["cases_file"])
    indexed = {row["case_id"]: row for row in cases}
    if len(indexed) != len(cases):
        raise ValueError("duplicate frozen case identity")
    return "sha256:" + digest.hexdigest(), indexed


def retained_pair(original, replayed, seed):
    scores = rows(replayed / "retrieval_scores.jsonl")
    before = rows(original / "retrieval_scores.jsonl")
    if len(scores) != 204 or len(before) != 204:
        raise ValueError("retained public 1k replay requires all 204 scored cases")
    indexed = {row["case_id"]: row for row in scores}
    previous = {row["case_id"]: row for row in before}
    if len(indexed) != 204 or len(previous) != 204 or set(indexed) != set(previous):
        raise ValueError("missing, duplicate or unexpected replay cases")
    for case_id, expected in previous.items():
        if indexed[case_id] != expected:
            raise ValueError(f"retained per-case score changed: {case_id}")
    summary = json.loads((replayed / "summary.json").read_text())
    if summary["case_count"] != 204 or summary["scored_case_count"] != 204 or summary["unmapped_source_refs"] != 0:
        raise ValueError("replayed summary is incomplete")
    original_summary = json.loads((original / "summary.json").read_text())
    if summary["seed_hash"] != original_summary["seed_hash"]:
        raise ValueError("retained seed identity changed")
    seed_hash, cases = frozen_cases(seed)
    if seed_hash != summary["seed_hash"]:
        raise ValueError("independent frozen seed hash mismatch")
    aggregate = verify_aggregates(scores, summary, "overall", rank_rates=True)
    suite = rows(replayed / "suite.jsonl")
    for axis, values in summary["slices"].items():
        selected = [indexed[row["case_id"]] for row in suite if axis in set(row.get("slices", [])) | set(cases[row["case_id"]].get("slices", []))]
        if not selected:
            raise ValueError("slice has no independently selected cases: " + axis)
        verify_aggregates(selected, values, axis, rank_rates=False)
    return {"case_count": 204, "seed_hash": summary["seed_hash"], "aggregate": aggregate,
            "original_score_sha256": file_hash(original / "retrieval_scores.jsonl"),
            "replayed_score_sha256": file_hash(replayed / "retrieval_scores.jsonl")}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--go-log", required=True, type=Path)
    parser.add_argument("--seed", type=Path)
    parser.add_argument("--original-run", action="append", type=Path, default=[])
    parser.add_argument("--replayed-run", action="append", type=Path, default=[])
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    if len(args.original_run) != len(args.replayed_run):
        raise ValueError("each original run requires exactly one replay")
    if args.original_run and args.seed is None:
        raise ValueError("retained replay verification requires --seed")
    versions = {name: importlib.metadata.version(name) for name in ("ir-measures", "pytrec-eval-terrier", "numpy", "scipy")}
    if versions["ir-measures"] != "0.4.3" or versions["pytrec-eval-terrier"] != "0.5.11":
        raise ValueError("install the pinned scoring reference requirements")
    result = {"schema_version": "dense-mem.eval.measurement-score-check.v1", "versions": versions,
              "reference": reference_examples(args.go_log), "replays": [retained_pair(a, b, args.seed) for a, b in zip(args.original_run, args.replayed_run)]}
    args.out.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({"verified": True, "reference_cases": result["reference"]["cases"], "replayed_runs": len(result["replays"])}))


if __name__ == "__main__":
    main()
