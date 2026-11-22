"""Compare completed hosted public seed build study reports, keyed by question and policy."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import statistics


DATASETS = {"hotpotqa", "musique", "2wikimultihopqa", "nq_rear", "popqa"}
POLICIES = {"single", "multi_turn"}
METRICS = ("recall_at_1", "recall_at_5", "recall_at_10", "all_support_retrieved",
           "initial_supporting_text_coverage", "final_supporting_text_coverage", "exact_match", "f1")


def require(condition, reason):
    if not condition:
        raise ValueError(reason)


def read_reports(directory, condition):
    outputs = {}
    for name in directory.rglob("scores.json"):
        raw = name.read_bytes()
        report = json.loads(raw)
        identity = report["identity"]
        if identity.get("condition") != condition:
            continue
        dataset = identity["dataset"]
        require(dataset in DATASETS and dataset not in outputs, "duplicate or unknown dataset")
        require(report["complete"] and report["capture_complete"] and report["model_verified_before_cutoff"], "incomplete report")
        require(report["completed_answers"] == report["expected_answers"] == 2000, "incomplete denominators")
        require(report["restored_rows"] == report["corpus_rows"] > 0 and identity["canonical_sha256"], "incomplete restore")
        require(re.fullmatch(r"[^@\s]+@sha256:[a-f0-9]{64}", identity["seed_image"]), "unpinned seed")
        cases = {}
        for row in report["scores"]:
            key = (row["question_id"], row["policy"])
            require(row["completed"] and row["condition"] == condition and key[0] and key[1] in POLICIES and key not in cases, "invalid case identity")
            require(all(0 <= row[m] <= 1 for m in METRICS), "invalid metric")
            require(0 <= row["valid_citations"] <= row["citations"], "invalid citation counts")
            cases[key] = row
        require(len(cases) == 2000 and len({k[0] for k in cases}) == 1000, "missing question/policy")
        outputs[dataset] = (report, cases, hashlib.sha256(raw).hexdigest())
    require(set(outputs) == DATASETS, "missing dataset")
    return outputs


def compare_public(control_dir, baseline_dir, candidate_dir, base_sha, candidate_sha):
    require(base_sha != candidate_sha and all(re.fullmatch("[a-f0-9]{40}", s) for s in [base_sha, candidate_sha]), "expected distinct source commits")
    controls = read_reports(control_dir, "off")
    base = read_reports(baseline_dir, "on")
    candidate = read_reports(candidate_dir, "on")
    results = {}
    for dataset in sorted(DATASETS):
        off, left, right = controls[dataset], base[dataset], candidate[dataset]
        identities = [report[0]["identity"] for report in (off, left, right)]
        for field in ("model", "embedding_model", "embedding_dimensions", "answer_workers",
                      "context_window_tokens", "canonical_sha256", "scoring_policy", "registry_sha256"):
            require(len({i[field] for i in identities}) == 1, "mismatched " + field)
        require(identities[1]["seed_image"] != identities[2]["seed_image"], "independent children required")
        parent = identities[0]["seed_image"]
        for child, sha in ((left, base_sha), (right, candidate_sha)):
            report = child[0]
            sealed = report["ontology_preparation"]
            require(sealed["parent_seed_image"] == report["identity"]["parent_seed_image"] == parent, "mismatched parent")
            require(sealed["source_revision"] == sha and sealed["image"] and sealed["attempts_sha256"] and sealed["ontology_sha256"], "missing build identity")
            counts = sealed["counts"]
            require(counts["eligible"] == counts["organized"] > 0 and not any(counts[k] for k in ("failed", "ambiguous", "pending")), "incomplete prepared ontology")
        lp, rp = (side[0]["ontology_preparation"] for side in (left,right))
        require(all(lp[k] == rp[k] for k in ("policy_version","workers","model","context_window_tokens")), "preparation settings changed")
        require(lp["workers"] == 8 and lp["context_window_tokens"] == 500000 and lp["model"] == "dense-mem-testing", "unexpected preparation policy")
        require(off[1].keys() == left[1].keys() == right[1].keys(), "unmatched question sets")
        result = {}
        for policy in sorted(POLICIES):
            keys = sorted(k for k in off[1] if k[1] == policy)
            axes = {}
            for m in METRICS:
                columns = [[float(side[1][k][m]) for k in keys] for side in (off,left,right)]
                deltas = [b-a for a,b in zip(columns[1],columns[2])]
                axes[m] = {"off":statistics.mean(columns[0]), "base_on":statistics.mean(columns[1]), "candidate_on":statistics.mean(columns[2]),
                           "negative":sum(v < 0 for v in deltas), "positive":sum(v > 0 for v in deltas),
                           "changed_questions":[k[0] for k,v in zip(keys,deltas) if v != 0]}
            cites = {}
            for label, side in (("off",off), ("base_on",left), ("candidate_on",right)):
                total = sum(side[1][k]["citations"] for k in keys)
                valid = sum(side[1][k]["valid_citations"] for k in keys)
                cites[label] = {"count":total,"valid":valid,"fraction":valid/total if total else None}
            citation_changes = [k[0] for k in keys if (left[1][k]["citations"],left[1][k]["valid_citations"]) != (right[1][k]["citations"],right[1][k]["valid_citations"])]
            result[policy] = {"metrics":axes,"citations":cites,"changed_citation_questions":citation_changes}
        results[dataset] = {"parent":parent,"child_images":[identities[1]["seed_image"],identities[2]["seed_image"]],
                            "score_artifact_sha256":[side[2] for side in (off,left,right)],"results":result}
    return results


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for field in ("off", "base_on", "candidate_on", "output"):
        parser.add_argument("--"+field.replace("_","-"), dest=field, type=Path, required=True)
    parser.add_argument("--base-sha",required=True)
    parser.add_argument("--candidate-sha",required=True)
    args = parser.parse_args()
    result = compare_public(args.off,args.base_on,args.candidate_on,args.base_sha,args.candidate_sha)
    args.output.write_text(json.dumps(result,indent=2)+"\n")
