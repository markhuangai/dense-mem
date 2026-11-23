"""Verify complete study evidence and decide whether a private run is warranted."""
import hashlib
import json
from pathlib import Path
import statistics
import sys

DATASETS = ("hotpotqa", "musique", "2wikimultihopqa", "nq_rear", "popqa")
PRIMARY = DATASETS[:2]
COUNTERS = (
    "FreshnessChecks", "Requeues", "RefreshedPending", "DiscoveryMS", "ValidationMS",
    "ProviderAttempts", "ReportedInput", "ReportedOutput", "UnknownUsage", "MarkerMatches", "BackfillChecks", "BackfillMS",
)


def require(value, reason):
    if not value:
        raise ValueError(reason)


def midpoint(values):
    return statistics.median(values)


def pair(control, candidate):
    ratios = [100 * (old - new) / old for old, new in zip(control, candidate, strict=True)]
    deltas = [old - new for old, new in zip(control, candidate, strict=True)]
    mad = midpoint([abs(item - midpoint(control)) for item in control])
    return {
        "median_percent_saved": midpoint(ratios),
        "median_ms_saved": midpoint(deltas),
        "faster": sum(x > 0 for x in deltas),
        "mad_ms": mad,
        "repeatable_regression": sum(x < 0 for x in deltas) >= 3 and -midpoint(deltas) > mad,
        "passed": all(x > 0 for x in deltas) and midpoint(deltas) > mad,
    }


def load(directory, name):
    raw = (directory / f"{name}.json").read_bytes()
    return json.loads(raw), hashlib.sha256(raw).hexdigest()


def report_drain(directory, dataset):
    data, digest = load(directory, dataset)
    require(data["Dataset"] == dataset and data["Protocol"] == "ontology-receipt-recheck-v1", "wrong workload")
    controls = ("base", "vector") if dataset in PRIMARY else ("base",)
    arms = (*controls, "receipt")
    groups = {arm: [] for arm in arms}
    require(len(data["TemplateSHA"]) == 64 and data["SeedSources"] > 0, "missing template receipt")
    require(set(data["FixedHashes"]) == set(arms) and len(set(data["FixedHashes"].values())) == 1, "fixed-schedule request drift")
    seen = set()
    for run in data["Runs"]:
        arm = run["Arm"]
        key = (run["Pair"], arm)
        require(key not in seen, "duplicate arm in pair")
        seen.add(key)
        cov = run["Coverage"]
        require(cov["eligible"] == cov["organized"] == data["SeedSources"], "incomplete organization")
        require(all(run[k] for k in ("DiscoveryComplete", "CoverageComplete", "Paused")), "invalid pause or coverage receipt")
        require(not any(run[k] for k in ("LiveLeases", "RunningBatches", "UnreconciledAttempts", "RemainingMarkers")), "nonterminal work")
        require(run["Quality"] == data["Runs"][0]["Quality"], "semantic or Recall drift")
        require(run["ElapsedMS"] > 0 and run["CorrectionMS"] > 0, "missing phase timing")
        groups[arm].append(run)
    for runs in groups.values():
        runs.sort(key=lambda run: run["Pair"])
        require([run["Pair"] for run in runs] == [1, 2, 3, 4], "missing matched run")
    def measure(arm, field):
        return [run[field] for run in groups[arm]]
    comps = {}
    for arm in controls:
        comps[arm] = {
            "full_drain": pair(measure(arm, "ElapsedMS"), measure("receipt", "ElapsedMS")),
            "correction": pair(measure(arm, "CorrectionMS"), measure("receipt", "CorrectionMS")),
        }
    base_checks = midpoint(measure("base", "FreshnessChecks"))
    avoided_checks = base_checks - midpoint([a + b for a, b in zip(measure("receipt", "FreshnessChecks"), measure("receipt", "BackfillChecks"), strict=True)])
    baseline_validation = midpoint(measure("base", "ValidationMS"))
    candidate_validation = midpoint([a + b for a, b in zip(measure("receipt", "ValidationMS"), measure("receipt", "BackfillMS"), strict=True)])
    return {
        "input_report_sha256": digest,
        "template_sha256": data["TemplateSHA"],
        "comparisons": comps,
        "phase_medians": {arm: {key: midpoint(measure(arm, key)) for key in COUNTERS} for arm in arms},
        "median_checks_avoided": avoided_checks,
        "median_validation_ms_avoided": baseline_validation - candidate_validation,
        "fraction_of_baseline_checks_avoided": avoided_checks / base_checks if base_checks else None,
    }


def compare(directory, source):
    jobs, _ = load(directory, "correctness")
    require(jobs["pass"] is True and jobs["study_source"] == source, "adverse-path proof missing")
    identity, _ = load(directory, "identity")
    require(identity["study_source"] == source and identity["base_source"].startswith("214fee2f"), "wrong source identity")
    scales = {}
    for factor in (1, 4, 16):
        report, digest = load(directory, f"scale-{factor}")
        require({entry["Fanout"] for entry in report} == {"disjoint", "narrow", "common", "dependent"}, "incomplete scale matrix")
        for entry in report:
            require(entry["CatalogFactor"] == factor and entry["CatalogSize"] == 128 * factor + 1, "topic curve cardinality drift")
            if entry["Fanout"] in {"narrow", "common"}:
                require(entry["ChecksAvoided"] == entry["StockMatches"] > 0 and not entry["CandidateMatches"], "unrelated topics ran full checks")
            if entry["Fanout"] == "dependent":
                require(entry["ChecksAvoided"] == 0 and entry["CandidateMatches"] > 0, "lost direct dependencies")
        scales[factor] = {"sha256": digest, "samples": report}
    results = {dataset: report_drain(directory, dataset) for dataset in DATASETS}
    primary_pass = all(
        check["full_drain"]["passed"]
        for dataset in PRIMARY for check in results[dataset]["comparisons"].values()
    )
    repeats_regress = any(
        metric["repeatable_regression"]
        for result in results.values() for check in result["comparisons"].values() for metric in check.values()
    )
    hotpot_target = results["hotpotqa"]["comparisons"]["base"]["full_drain"]["median_percent_saved"] >= 13
    return {
        **identity,
        "advance": primary_pass and not repeats_regress and hotpot_target,
        "evidence_complete": True,
        "scaling": scales,
        "datasets": results,
    }


if __name__ == "__main__":
    directory = Path(sys.argv[1])
    try:
        report = compare(directory, sys.argv[2])
    except (OSError, KeyError, ValueError, TypeError) as error:
        report = {"evidence_complete": False, "advance": False, "error": str(error)}
    (directory / "comparison.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Ontology receipt study", "advance" if report["advance"] else "hold", flush=True)
    if not report["evidence_complete"]:
        sys.exit(report["error"])
