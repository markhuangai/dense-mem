"""Run the registered ontology quality cohorts or summarize paired hosted build results."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import statistics
import subprocess
import sys
import tempfile


def cohorts(arm, directory):
    root = Path.cwd()
    source = root / "internal/ontology/postgres/organization.go"
    original = source.read_text()
    anchor = "AND NOT head.retired AND head.kind IN ('topic')"
    if original.count(anchor) != 1:
        raise SystemExit("vocabulary query shape changed")
    directory.mkdir(exist_ok=True, parents=True)
    report = {"arm": arm, "source": os.environ["GITHUB_SHA"], "tests": {}}
    with tempfile.TemporaryDirectory() as temp:
        command = ["go", "test", "-json", "-count=1", "-tags=integration", "-p=1", "-timeout=70m"]
        if arm == "topic":
            variant = Path(temp) / "organization.go"
            variant.write_text(original.replace(anchor, anchor + " AND revision.kind = 'topic'"))
            overlay = Path(temp) / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(source): str(variant)}}))
            command.extend(["-overlay", str(overlay)])
        required = [
            "TestOntologyOrganizationCohort", "TestOntologyMaintenanceMatchingCohort",
            "TestOntologyOrganizationBatchedVocabulary", "TestOntologyOrganizationBatchedPublicationFences",
            "TestOntologyMaintenanceRevalidationDiscoveryConcurrency", "TestOntologyMaintenanceRevalidationDiscoveryIsolation",
            "TestRecallAccuracyCohort",
        ]
        command += ["-run", "^(" + "|".join(required) + ")$", "./internal/ontology/postgres", "./internal/recall/postgres"]
        p = subprocess.Popen(command, stdout=subprocess.PIPE, text=True)
        fragments = {}
        recall_cases = []
        for line in p.stdout:
            print(line, end="")
            record = json.loads(line)
            test = record.get("Test", "")
            if record["Action"] == "output" and test.startswith("TestRecallAccuracyCohort/"):
                text = fragments.get(test, "") + record.get("Output", "")
                lines = text.split("\n")
                fragments[test] = lines.pop()
                for entry in lines:
                    marker = "RECALL_ACCURACY_CASE "
                    if marker in entry:
                        case = json.loads(entry.split(marker, 1)[1])
                        recall_cases.append([test, case["case_id"], case["ontology_enabled"],
                                             case["ranked_sources"], case["score"], case["ranking_scores"],
                                             case["search_state"], case["canonical_preserved"]])
            if test in required and record["Action"] in ("pass", "skip", "fail"):
                report["tests"][test] = record["Action"]
        status = p.wait()
        if status == 0 and len(recall_cases) != 96:
            raise ValueError("expected two repetitions of both conditions for 24 gold cases")
        recall_cases.sort(key=lambda x: x[0])
        report["recall_cases"] = len(recall_cases)
        report["recall_sha256"] = hashlib.sha256(json.dumps(recall_cases,sort_keys=True).encode()).hexdigest()
        if arm == "topic" and status == 0:
            base = json.loads((directory / "cohorts-base.json").read_text())
            if report["recall_sha256"] != base["recall_sha256"]:
                raise ValueError("ranked Evidence or accuracy metrics changed")
        (directory / ("cohorts-" + arm + ".json")).write_text(json.dumps(report, indent=2) + "\n")
        if status or any(report["tests"].get(test) != "pass" for test in required):
            raise SystemExit("incomplete real PostgreSQL quality cohorts")


def compare(directory, datasets):
    passed = True
    all_reports = {}
    for name in datasets:
        p = directory / (name + ".json")
        data = json.loads(p.read_bytes())
        runs = data["Runs"]
        if data["Protocol"] != "ontology-build-drain-v1" or data["Shape"]["Dataset"] != name:
            raise ValueError("workload identity mismatch")
        groups = {(r["Pair"], r["Arm"]): r for r in runs}
        expected = {(pair, arm) for pair in range(1,5) for arm in ("base","topic")}
        if len(runs) != 8 or set(groups) != expected or not data["FixedInputSHA"]:
            raise ValueError("incomplete paired evidence for " + name)
        base = [groups[i, "base"]["ElapsedMS"] for i in range(1,5)]
        candidate = [groups[i, "topic"]["ElapsedMS"] for i in range(1,5)]
        if min(base + candidate) <= 0:
            raise ValueError("nonpositive elapsed time")
        for r in runs:
            c = r["Coverage"]
            if (c["eligible"] <= 0 or c["organized"] != c["eligible"] or any(c[k] for k in ("pending","ambiguous","failed"))
                    or r["RemainingMarkers"] or r["Quality"] != runs[0]["Quality"] or r["CorrectionMS"] <= 0):
                raise ValueError("incomplete coverage or quality for " + name)
        savings = [a-b for a,b in zip(base,candidate)]
        median_savings = statistics.median(savings)
        repeat_noise = statistics.median(abs(x-statistics.median(base)) for x in base)
        consistent = all(x > 0 for x in savings) and median_savings > repeat_noise
        bc = [groups[i,"base"]["CorrectionMS"] for i in range(1,5)]
        cc = [groups[i,"topic"]["CorrectionMS"] for i in range(1,5)]
        correction_slower = all(x > y for x,y in zip(cc,bc)) and statistics.median(cc)-statistics.median(bc) > statistics.median(abs(x-statistics.median(bc)) for x in bc)
        passes = consistent and not correction_slower
        passed = passed and passes
        all_reports[name] = {"report_sha256":hashlib.sha256(p.read_bytes()).hexdigest(), "base_ms":base, "candidate_ms":candidate, "paired_saving_ms":savings,
                             "base_mad_ms":repeat_noise, "median_paired_speedup":statistics.median(a/b for a,b in zip(base,candidate)),
                             "base_correction_ms":bc,"candidate_correction_ms":cc,"consistent_speedup":consistent,"correction_regression":correction_slower,
                             "advance":passes}
    out = {"datasets":all_reports,"advance":passed}
    print(json.dumps(out,indent=2))
    (directory / "comparison.json").write_text(json.dumps(out, indent=2)+"\n")
    if os.getenv("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as dest:
            dest.write("advance="+str(passed).lower()+"\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    cohort = sub.add_parser("cohorts")
    cohort.add_argument("arm", choices=["base","topic"])
    cohort.add_argument("directory", type=Path)
    comparison = sub.add_parser("compare")
    comparison.add_argument("directory", type=Path)
    comparison.add_argument("datasets", nargs="+")
    args = parser.parse_args()
    if args.command == "cohorts":
        cohorts(args.arm,args.directory)
    else:
        compare(args.directory,args.datasets)
