"""Run the registered ontology quality cohorts or summarize paired hosted build results."""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import re
import statistics
import subprocess
import sys
import tempfile


def vector_ddl():
    source = Path("internal/ontology/postgres/source_queries.go").read_text()
    expression = re.search(r"const vocabularyVectorSQL = `([^`]+)`", source).group(1)
    source = Path("internal/ontology/contract/matching.go").read_text()
    words = re.search(r"const vocabularyStopWords = `([^`]+)`", source).group(1).split()
    if not words or not all(re.fullmatch("[a-z]+", w) for w in words):
        raise ValueError("unsupported stop-word source")
    for old, new in (("head.names", "names"),
                     ("COALESCE(revision.body->'definition'->>'description','')", "description"),
                     ("?::text[]", "ARRAY["+",".join("'"+w+"'" for w in words)+"]::text[]")):
        if expression.count(old) != 1:
            raise ValueError("unexpected vocabulary vector construction")
        expression = expression.replace(old, new)
    template = Path("tests/eval/scripts/ontology_build_vectors.sql").read_text()
    if template.count("__STUDY_VECTOR_EXPRESSION__") != 1:
        raise ValueError("missing study SQL expression placeholder")
    return template.replace("__STUDY_VECTOR_EXPRESSION__", expression)


def vector_source(original):
    start = original.index("func (s *Store) organizationVocabulary(")
    end = original.index("\nfunc ", start + 1)
    block = original[start:end]
    join = "JOIN ontology_revision_records AS revision USING(team_id,shared_space_id,space_generation,record_id,version)"
    if block.count(join) != 1 or block.count("+vocabularyVectorSQL+") != 2:
        raise ValueError("unexpected vocabulary lookup")
    joined = join+" JOIN ontology_study_topic_vectors AS studyvec USING(team_id,shared_space_id,space_generation,record_id,version)"
    block = block.replace(join, joined).replace("+vocabularyVectorSQL+", '+"ontology_study_cached(studyvec.document,?::text[])"+')
    return original[:start]+block+original[end:]


LOOKUP_ARMS = ("base", "vector", "rank-first")
LOOKUP_ORDERS = (
    ("base", "vector", "rank-first"),
    ("rank-first", "vector", "base"),
    ("vector", "rank-first", "base"),
    ("base", "rank-first", "vector"),
)
COHORTS = (
    "TestOntologyOrganizationCohort",
    "TestOntologyMaintenanceMatchingCohort",
    "TestOntologyOrganizationBatchedVocabulary",
    "TestOntologyOrganizationBatchedPublicationFences",
    "TestOntologyMaintenanceRevalidationDiscoveryConcurrency",
    "TestOntologyMaintenanceRevalidationDiscoveryIsolation",
    "TestRecallAccuracyCohort",
)
LOOKUP_REGRESSIONS = ("TestOntologyRankFirstVocabulary", "TestOntologyVocabularyUsesCurrentCandidateWindow")


def lookup_overlay(directory):
    source = Path("internal/ontology/postgres/organization.go").resolve()
    original = source.read_text()
    vector = vector_source(original)

    def literal(text):
        start = text.index("tx.Raw(", text.index("func (s *Store) organizationVocabulary(")) + len("tx.Raw(")
        end = text.index("LIMIT ?`,", start) + len("LIMIT ?`")
        return start, end, text[start:end]

    start, end, base_sql = literal(original)
    _, _, vector_sql = literal(vector)
    key = "team_id,shared_space_id,space_generation,record_id,version"
    rank_sql = vector_sql.replace(
        "revision.body,head.record_id,",
        "head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version,",
        1,
    ).replace(f"JOIN ontology_revision_records AS revision USING({key}) ", "", 1)
    page_start = "   SELECT body,forced,exact,rank,record_id::text FROM ranked"
    if rank_sql.count(page_start) != 1 or "ontology_revision_records" in rank_sql:
        raise ValueError("unexpected rank-first starting SQL")
    rank_sql = rank_sql.replace(page_start, """  ,page AS MATERIALIZED (
   SELECT * FROM ranked""", 1).removesuffix("`") + """
  )
  SELECT (
    SELECT revision.body FROM ontology_revision_records AS revision
    WHERE revision.kind='topic' AND
      (revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id,revision.version)=
      (page.team_id,page.shared_space_id,page.space_generation,page.record_id,page.version)
  ),page.forced,page.exact,page.rank,page.record_id::text
  FROM page ORDER BY page.forced DESC,page.exact DESC,page.rank DESC,page.record_id`"""

    # Only the SQL choice is overlaid. Every arm shares the original scanner, cursor and freshness logic.
    patched = original[:start] + "studyVocabularyLookup()" + original[end:]
    tracked_rows = "rows, err := tx.Raw(studyVocabularyLookup(),"
    patched = patched.replace(tracked_rows, "lookup := studyVocabularyLookup()\n\t\trows, err := tx.Raw(lookup,", 1)
    pos = patched.index("\n\t\tcandidates := []vocabularyCandidate{}", start)
    patched = patched[:pos] + "\n\t\tstudyWitnessVocabulary(lookup)" + patched[pos:]
    patched = patched.replace('import (', 'import (\n "crypto/sha256"\n "fmt"\n "os"\n "sync"', 1)
    patched += """
var studyVocabularyWitness sync.Once

func studyWitnessVocabulary(query string) {
    if os.Getenv("ONTOLOGY_STUDY_WITNESS") == "1" {
        studyVocabularyWitness.Do(func() {
            fmt.Printf("ONTOLOGY_STUDY_LOOKUP %s %x\\n", os.Getenv("ONTOLOGY_STUDY_ACTIVE_ARM"), sha256.Sum256([]byte(query)))
        })
    }
}

func studyVocabularyLookup() string {
    switch os.Getenv("ONTOLOGY_STUDY_ACTIVE_ARM") {
    case "vector":
        return """ + vector_sql + """
    case "rank-first":
        return """ + rank_sql + """
    default:
        return """ + base_sql + """
    }
}
"""
    directory.mkdir(exist_ok=True, parents=True)
    variant = directory.resolve() / source.name
    variant.write_text(patched)
    overlay = directory / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(source): str(variant)}}))
    return overlay


def page_overlay(directory):
    source = Path("internal/ontology/postgres/maintenance_discovery.go").resolve()
    original = source.read_text()
    anchor = "\t\tif kind < len(maintenanceSourceKinds) {"
    if original.count(anchor) != 1 or original.count('"strings"') != 1:
        raise ValueError("unexpected discovery source")
    invocation = """\t\tselectedLimit, err := studyDiscoveryPageLimit(tx, fence, kind, turn, limit)
\t\tif err != nil {
\t\t\treturn err
\t\t}
\t\tlimit = selectedLimit
"""
    directory.mkdir(exist_ok=True, parents=True)
    variant = directory.resolve() / source.name
    function = Path("tests/eval/scripts/ontology_build_pages.go.inc").read_text()
    variant.write_text(original.replace('"strings"', '"os"\n\t"strings"').replace(anchor, invocation + anchor) + "\n" + function)
    overlay = directory / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(source): str(variant)}}))
    return overlay


@contextlib.contextmanager
def study_migration(arm):
    if arm not in ("vector", "rank-first"):
        yield
        return
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise ValueError("the candidate migration belongs to a disposable hosted testing checkout")
    path = Path("migrations/postgres/v2_8/20991122010000_study_topic_vectors.sql")
    migration = ("-- +goose Up\n-- +goose StatementBegin\n"+vector_ddl()+"\n-- +goose StatementEnd\n"
                 "-- +goose Down\nDROP TRIGGER ontology_study_publish_vector ON ontology_record_heads;\n"
                 "DROP FUNCTION ontology_study_publish_vector();\nDROP TABLE ontology_study_topic_vectors;\n"
                 "DROP FUNCTION ontology_study_cached(tsvector,text[]);\nDROP FUNCTION ontology_study_make_vector(text[],text);\n")
    with path.open("x") as f:
        f.write(migration)
    try:
        yield
    finally:
        path.unlink()


def cohorts(arm, directory):
    root = Path.cwd()
    source = root / "internal/ontology/postgres/organization.go"
    original = source.read_text()
    anchor = "AND NOT head.retired AND head.kind IN ('topic')"
    if original.count(anchor) != 1:
        raise SystemExit("vocabulary query shape changed")
    directory.mkdir(exist_ok=True, parents=True)
    report = {"arm": arm, "source": os.environ["GITHUB_SHA"], "run_id": os.environ["GITHUB_RUN_ID"],
              "run_attempt": os.environ["GITHUB_RUN_ATTEMPT"], "tests": {}}
    with tempfile.TemporaryDirectory() as temp, study_migration(arm):
        command = ["go", "test", "-json", "-count=1", "-tags=integration", "-p=1", "-timeout=70m"]
        if arm in LOOKUP_ARMS:
            command.extend(["-overlay", str(lookup_overlay(Path(temp)))])
            command[command.index("-tags=integration")] = "-tags=integration,ontology_study"
        elif arm == "topic":
            variant = Path(temp) / "organization.go"
            variant.write_text(original.replace(anchor, anchor + " AND revision.kind = 'topic'"))
            overlay = Path(temp) / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(source): str(variant)}}))
            command.extend(["-overlay", str(overlay)])
        elif arm == "pages":
            command.extend(["-overlay", str(page_overlay(Path(temp)))])
        required = list(COHORTS)
        if arm in LOOKUP_ARMS:
            required.extend(LOOKUP_REGRESSIONS)
        command += ["-run", "^(" + "|".join(required) + ")$", "./internal/ontology/postgres", "./internal/recall/postgres"]
        environment = {**os.environ, "ONTOLOGY_STUDY_ACTIVE_ARM": arm, "ONTOLOGY_STUDY_WITNESS": "1"}
        p = subprocess.Popen(command, stdout=subprocess.PIPE, text=True, env=environment)
        fragments = {}
        recall_cases = []
        for line in p.stdout:
            print(line, end="")
            record = json.loads(line)
            test = record.get("Test", "")
            output = record.get("Output", "")
            witness = re.search(rf"ONTOLOGY_STUDY_LOOKUP {arm} ([0-9a-f]{{64}})", output)
            if witness:
                report["lookup_sha256"] = witness[1]
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
        (directory / ("cohorts-" + arm + ".json")).write_text(json.dumps(report, indent=2) + "\n")
        if status or any(report["tests"].get(test) != "pass" for test in required):
            raise SystemExit("incomplete real PostgreSQL quality cohorts")
        if arm in LOOKUP_ARMS and "lookup_sha256" not in report:
            raise ValueError("cohorts did not witness the selected lookup")
        if arm != "base":
            base = json.loads((directory / "cohorts-base.json").read_text())
            if report["recall_sha256"] != base["recall_sha256"]:
                raise ValueError("ranked Evidence or accuracy metrics changed")


def compare_scheduled_inputs(streams):
    for arm, size in (("base", 100), ("pages", 25)):
        turns = streams[arm]
        if len(turns) != 3 or turns[0]["PageLimit"] != size or not any(t["Requests"] for t in turns):
            raise ValueError("incomplete scheduled probe for " + arm)
    changes = []
    for index, (left, right) in enumerate(zip(streams["base"], streams["pages"]), start=1):
        fields = [f for f in ("Sources", "Candidates") if left[f] != right[f]]
        requests = [[{k: v for k, v in r.items() if k != "request_id"} for r in turn["Requests"]] for turn in (left, right)]
        if requests[0] != requests[1]:
            fields.append("Requests")
        if fields:
            changes.append({"turn": index, "different": fields})
    return changes


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
        variant = data.get("Candidate", "topic")
        if variant not in ("topic","vector","pages"):
            raise ValueError("unknown study candidate")
        schedule_changes = compare_scheduled_inputs(data["ScheduledInputs"]) if variant == "pages" else None
        expected = {(pair, arm) for pair in range(1,5) for arm in ("base",variant)}
        if len(runs) != 8 or set(groups) != expected or not data["FixedInputSHA"]:
            raise ValueError("incomplete paired evidence for " + name)
        base = [groups[i, "base"]["ElapsedMS"] for i in range(1,5)]
        candidate = [groups[i, variant]["ElapsedMS"] for i in range(1,5)]
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
        cc = [groups[i,variant]["CorrectionMS"] for i in range(1,5)]
        correction_slower = all(x > y for x,y in zip(cc,bc)) and statistics.median(cc)-statistics.median(bc) > statistics.median(abs(x-statistics.median(bc)) for x in bc)
        passes = consistent and not correction_slower
        passed = passed and passes
        all_reports[name] = {"candidate":variant,"report_sha256":hashlib.sha256(p.read_bytes()).hexdigest(), "base_ms":base, "candidate_ms":candidate, "paired_saving_ms":savings,
                             "base_mad_ms":repeat_noise, "median_paired_speedup":statistics.median(a/b for a,b in zip(base,candidate)),
                             "base_correction_ms":bc,"candidate_correction_ms":cc,"consistent_speedup":consistent,"correction_regression":correction_slower,
                             "scheduled_input_differences":schedule_changes, "advance":passes}
    out = {"datasets":all_reports,"advance":passed}
    print(json.dumps(out,indent=2))
    (directory / "comparison.json").write_text(json.dumps(out, indent=2)+"\n")
    if os.getenv("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as dest:
            dest.write("advance="+str(passed).lower()+"\n")


def require_study(condition, reason):
    if not condition:
        raise ValueError(reason)


def study_receipt(path):
    raw = path.read_bytes()
    return json.loads(raw), hashlib.sha256(raw).hexdigest()


def valid_sha(value, size=64):
    return re.fullmatch(f"[0-9a-f]{{{size}}}", value or "") is not None


def verify_lookup_schedule(schedules):
    require_study(set(schedules) == set(LOOKUP_ARMS), "missing fixed-schedule arm")
    for arm, turns in schedules.items():
        require_study(len(turns) == 3, f"{arm}: expected three scheduled turns")
        require_study(any(t["HTTPBodies"] for t in turns), f"{arm}: missing emitted requests")
        for index, (control, turn) in enumerate(zip(schedules["base"], turns), start=1):
            require_study(turn["PageLimit"] == 100 and turn["Sources"] and turn["Candidates"],
                          f"{arm} turn {index}: missing measured page or ordered inputs")
            for field in ("Sources", "Candidates", "HTTPBodies"):
                require_study(turn[field] == control[field], f"{arm} turn {index}: {field} drift")


def paired_lookup_savings(control, variant):
    base = [r["ElapsedMS"] for r in control]
    candidate = [r["ElapsedMS"] for r in variant]
    savings = [a - b for a, b in zip(base, candidate)]
    percentages = [100 * d / a for d, a in zip(savings, base)]
    mad = statistics.median(abs(x - statistics.median(base)) for x in base)
    base_correction = [r["CorrectionMS"] for r in control]
    candidate_correction = [r["CorrectionMS"] for r in variant]
    correction_mad = statistics.median(abs(x - statistics.median(base_correction)) for x in base_correction)
    correction_delta = statistics.median(candidate_correction) - statistics.median(base_correction)
    correction_regression = all(b > a for a, b in zip(base_correction, candidate_correction)) and correction_delta > correction_mad
    median_savings = statistics.median(savings)
    return {
        "control_elapsed_ms": base,
        "candidate_elapsed_ms": candidate,
        "paired_saving_ms": savings,
        "paired_saving_percent": percentages,
        "median_paired_saving_ms": median_savings,
        "median_paired_saving_percent": statistics.median(percentages),
        "control_mad_ms": mad,
        "faster_pairs": sum(value > 0 for value in savings),
        "control_correction_ms": base_correction,
        "candidate_correction_ms": candidate_correction,
        "control_correction_mad_ms": correction_mad,
        "correction_regression": correction_regression,
        "passes_screen": all(value > 0 for value in savings) and median_savings > mad and not correction_regression,
    }


def compare_lookup_dataset(data, name, source, witnesses):
    require_study(data["Protocol"] == "ontology-build-rank-first-v1" and data["Shape"]["Dataset"] == name,
                  f"{name}: study identity mismatch")
    require_study((data["Source"], data["RunID"], data["RunAttempt"]) == source and data["Runner"],
                  f"{name}: runner or source identity mismatch")
    require_study(data["PostgresVersion"].startswith("18.") and valid_sha(data["TemplateSHA"])
                  and valid_sha(data["CorrectionSHA"].removeprefix("sha256:")), f"{name}: missing frozen input identities")
    require_study(data["Stage"] == "complete", f"{name}: stopped at {data['Stage']}")
    plans = data["Plans"]
    require_study(set(plans) == set(LOOKUP_ARMS), f"{name}: missing arm query plan")
    require_study(set(p["BindingsSHA"] for p in plans.values()) == {plans["base"]["BindingsSHA"]}
                  and valid_sha(plans["base"]["BindingsSHA"].removeprefix("sha256:")), f"{name}: EXPLAIN bindings changed")
    for arm, report in plans.items():
        require_study(report["QuerySHA"] == witnesses[arm] and report["Plan"]["Raw"], f"{name}/{arm}: missing actual query plan")
    require_study(plans["rank-first"]["Plan"]["PageBeforePayload"], f"{name}: payload work below ranking limit")
    verify_lookup_schedule(data["ScheduledInputs"])

    runs = data["Runs"]
    expected = [(i, arm) for i, order in enumerate(LOOKUP_ORDERS, start=1) for arm in order]
    require_study([(r["Pair"], r["Arm"]) for r in runs] == expected, f"{name}: incomplete or misordered repetitions")
    groups = {arm: [] for arm in LOOKUP_ARMS}
    for r in runs:
        arm = r["Arm"]
        counts = r["Coverage"]
        scope = f"{name} repetition {r['Pair']} {arm}"
        require_study(counts["eligible"] == counts["organized"] == data["SeedSources"] > 0
                      and all(r[k] for k in ("Paused", "DiscoveryComplete", "CoverageComplete"))
                      and not any(counts[k] for k in ("pending", "ambiguous", "failed"))
                      and not any(r[k] for k in ("RemainingMarkers", "ActiveWork", "LiveLeases",
                                                  "RunningBatches", "UnreconciledAttempts")),
                      f"{scope}: incomplete coverage or drain")
        require_study(r["Quality"] == runs[0]["Quality"] and r["LookupSHA"] == witnesses[arm]
                      and r["FullPages"] == r["Pages"] > 0 and r["LastPageSize"] == 100
                      and min(r["ElapsedMS"], r["CorrectionMS"], r["ProviderAttempts"]) > 0,
                      f"{scope}: incomplete quality or runtime checks")
        require_study((r["VectorInstall"]["Rows"] > 0) == (arm != "base"), f"{scope}: wrong projection setup")
        groups[arm].append(r)
    return {
        "template_sha256": data["TemplateSHA"],
        "correction_sha256": data["CorrectionSHA"],
        "runner": data["Runner"],
        "postgres_version": data["PostgresVersion"],
        "rank_first_vs_original": paired_lookup_savings(groups["base"], groups["rank-first"]),
        "rank_first_vs_stored_vector": paired_lookup_savings(groups["vector"], groups["rank-first"]),
        "stored_vector_vs_original": paired_lookup_savings(groups["base"], groups["vector"]),
        "query_plans": {arm: {k: v for k, v in p["Plan"].items() if k != "Raw"} for arm, p in plans.items()},
    }


def compare_lookups(directory, datasets):
    source = (os.environ["GITHUB_SHA"], os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"])
    out = {"source": source[0], "run_id": source[1], "run_attempt": source[2],
           "datasets": {}, "cohorts": {}, "advance": False, "outcome": "inconclusive"}
    error = None
    try:
        witnesses = {}
        for arm in LOOKUP_ARMS:
            cohort, sha = study_receipt(directory / f"cohorts-{arm}.json")
            require_study((cohort["source"], cohort["run_id"], cohort["run_attempt"]) == source and cohort["arm"] == arm,
                          f"{arm}: quality run identity mismatch")
            require_study(cohort["tests"] == dict.fromkeys(COHORTS + LOOKUP_REGRESSIONS, "pass")
                          and cohort["recall_cases"] == 96 and valid_sha(cohort["recall_sha256"])
                          and valid_sha(cohort["lookup_sha256"]), f"{arm}: incomplete cohort evidence")
            witnesses[arm] = cohort["lookup_sha256"]
            out["cohorts"][arm] = {"artifact_sha256": sha, "ordered_recall_sha256": cohort["recall_sha256"]}
        require_study(len(set(witnesses.values())) == 3, "expected distinct original, vector and rank-first SQL")
        require_study(len({c["ordered_recall_sha256"] for c in out["cohorts"].values()}) == 1, "ordered Recall drift")
        for name in datasets:
            data, sha = study_receipt(directory / f"{name}.json")
            result = compare_lookup_dataset(data, name, source, witnesses)
            out["datasets"][name] = {**result, "artifact_sha256": sha}
        require_study(os.getenv("ONTOLOGY_STUDY_PREREQUISITES", "success") == "success", "a required hosted job failed")
        results = out["datasets"]
        advances = all(x["rank_first_vs_original"]["passes_screen"] for x in results.values())
        incremental = all(results[name]["rank_first_vs_stored_vector"]["passes_screen"] for name in ("hotpotqa", "musique"))
        target = results["hotpotqa"]["rank_first_vs_original"]["median_paired_saving_percent"] >= 13
        five_shapes = len(results) == 5
        out.update(
            advance=advances,
            hotpotqa_target_met=target,
            incremental_gate_passed=incremental,
            stored_vector_screen_passed=all(x["stored_vector_vs_original"]["passes_screen"] for x in results.values()),
            outcome=("qualifies-for-production-approval" if advances and incremental and target else "hold")
            if five_shapes else ("advance-to-remaining" if advances else "hold"),
        )
    except (KeyError, OSError, TypeError, ValueError) as exc:
        error = str(exc)
        out["error"] = error
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "comparison.json").write_text(json.dumps(out, indent=2) + "\n")
    if os.getenv("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as dest:
            dest.write(f"advance={str(out['advance']).lower()}\n")
    print(f"Ontology study {out['outcome']}")
    if error:
        raise SystemExit(error)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    cohort = sub.add_parser("cohorts")
    cohort.add_argument("arm", choices=[*LOOKUP_ARMS, "topic", "pages"])
    cohort.add_argument("directory", type=Path)
    comparison = sub.add_parser("compare")
    comparison.add_argument("directory", type=Path)
    comparison.add_argument("datasets", nargs="+")
    rank_comparison = sub.add_parser("compare-lookups")
    rank_comparison.add_argument("directory", type=Path)
    rank_comparison.add_argument("datasets", nargs="+")
    render = sub.add_parser("render-vector")
    render.add_argument("output", type=Path)
    pages = sub.add_parser("render-pages")
    pages.add_argument("directory", type=Path)
    lookup = sub.add_parser("render-lookup")
    lookup.add_argument("directory", type=Path)
    args = parser.parse_args()
    if args.command == "render-vector":
        args.output.write_text(vector_ddl())
    elif args.command == "render-pages":
        page_overlay(args.directory)
    elif args.command == "render-lookup":
        lookup_overlay(args.directory)
    elif args.command == "cohorts":
        cohorts(args.arm,args.directory)
    elif args.command == "compare-lookups":
        compare_lookups(args.directory, args.datasets)
    else:
        compare(args.directory,args.datasets)
