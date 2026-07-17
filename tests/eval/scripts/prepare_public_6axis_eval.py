#!/usr/bin/env python3
"""Prepare validated six-axis public semantic eval seeds."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
import shutil
from dataclasses import asdict
from pathlib import Path
from typing import Any

import prepare_full_public_rag_eval as beir
import prepare_public_semantic_eval as semantic


SCHEMA_VERSION = "dense-mem.eval.seed.v1"
VALIDATION_SCHEMA_VERSION = "dense-mem.eval.validation.v1"
ID_NAMESPACE = "public_6axis_v1"
SOURCE_LOCK_FILE = "source_locks/public_6axis_v1.json"
LOCKED_ARTIFACTS = {
    "scifact": Path("data/beir/scifact.zip"),
    "msmarco": Path("data/beir/msmarco.zip"),
    "hotpotqa": Path("data/beir/hotpotqa.zip"),
    "musique": Path("data/public_semantic/musique_v1.0.zip"),
    "qasper": Path("data/public_semantic/qasper-train-dev-v0.3.tgz"),
    "longmem_oracle": Path("data/public_semantic/longmemeval_s_cleaned.json"),
}

AXIS_BUDGETS = {
    1000: {
        "scifact": 90,
        "msmarco": 90,
        "hotpotqa": 90,
        "musique": 160,
        "qasper": 300,
        "longmem_oracle": 270,
    },
    5000: {
        "scifact": 500,
        "msmarco": 500,
        "hotpotqa": 500,
        "musique": 1000,
        "qasper": 1400,
        "longmem_oracle": 1100,
    },
}

BEIR_AXES = {
    "scifact": beir.AXES["beir_standard"],
    "msmarco": beir.AXES["msmarco_passage"],
    "hotpotqa": beir.AXES["hotpotqa_multihop"],
}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default="tests/eval")
    parser.add_argument("--size", type=int, choices=sorted(AXIS_BUDGETS), required=True)
    parser.add_argument("--seed-id", default="")
    parser.add_argument("--force", action="store_true")
    parser.add_argument("--no-download", action="store_true")
    parser.add_argument("--max-evidence-codepoints", type=int, default=semantic.MAX_EVIDENCE_CODEPOINTS)
    parser.add_argument("--beir-base-url", default=beir.DEFAULT_BEIR_BASE_URL)
    args = parser.parse_args()

    if args.max_evidence_codepoints != semantic.MAX_EVIDENCE_CODEPOINTS:
        parser.error("six-axis seeds must use max evidence length 999")

    root = Path(args.root)
    seed_id = args.seed_id.strip() or f"public_6axis_{args.size // 1000}k_v1"
    if args.size == 1000:
        seed_id = args.seed_id.strip() or "public_6axis_1k_v1"
    elif args.size == 5000:
        seed_id = args.seed_id.strip() or "public_6axis_5k_v1"

    seed_dir = root / "seeds" / seed_id
    suite_path = root / "suites" / f"{seed_id}.jsonl"
    stage_dir = root / "seeds" / f".{seed_id}.staging"
    stage_suite = root / "suites" / f".{seed_id}.jsonl.staging"

    if seed_dir.exists() and not args.force:
        raise SystemExit(f"{seed_dir} already exists; use --force to rebuild")
    if stage_dir.exists():
        shutil.rmtree(stage_dir)
    if stage_suite.exists():
        stage_suite.unlink()
    stage_dir.mkdir(parents=True, exist_ok=True)
    stage_suite.parent.mkdir(parents=True, exist_ok=True)

    generated = build_seed(root, seed_id, args.size, args, stage_dir, stage_suite)
    validate_generated(generated, expected_size=args.size)
    write_seed(stage_dir, stage_suite, seed_id, args.size, generated)
    seed_hash = stable_seed_hash(stage_dir)
    write_validation_report(stage_dir, seed_id, args.size, seed_hash, generated)

    if seed_dir.exists():
        shutil.rmtree(seed_dir)
    os.replace(stage_dir, seed_dir)
    os.replace(stage_suite, suite_path)
    print(f"wrote {seed_dir}")
    print(f"wrote {suite_path}")
    return 0


def build_seed(root: Path, seed_id: str, size: int, args: argparse.Namespace, stage_dir: Path, stage_suite: Path) -> dict[str, Any]:
    budgets = AXIS_BUDGETS[size]
    out = empty_generated()
    for axis_name in ("scifact", "msmarco", "hotpotqa"):
        add_beir_axis(out, root, seed_id, axis_name, budgets[axis_name], args)
    semantic.ensure_sources(root / "data" / "public_semantic", allow_download=not args.no_download)
    for axis_name in ("musique", "qasper", "longmem_oracle"):
        verify_locked_artifact(axis_name, root / LOCKED_ARTIFACTS[axis_name], source_lock_entry(root, axis_name))
    semantic_cases = semantic.build_cases(
        data_root=root / "data" / "public_semantic",
        seed_id=ID_NAMESPACE,
        max_codepoints=args.max_evidence_codepoints,
        musique_distractors=4,
        qasper_distractors=4,
        longmem_distractor_sessions=4,
        preflight=False,
    )
    add_semantic_axis(out, "musique", budgets["musique"], [c for c in semantic_cases if "musique" in c.slices])
    add_semantic_axis(out, "qasper", budgets["qasper"], [c for c in semantic_cases if "qasper" in c.slices])
    add_semantic_axis(out, "longmem_oracle", budgets["longmem_oracle"], [c for c in semantic_cases if "longmemeval_s" in c.slices])
    return out


def verify_source_lock(root: Path) -> None:
    lock_path = root / SOURCE_LOCK_FILE
    if not lock_path.exists():
        raise SystemExit(f"missing source lock {lock_path}")
    lock = json.loads(lock_path.read_text(encoding="utf-8"))
    if lock.get("lock_id") != ID_NAMESPACE:
        raise SystemExit(f"{lock_path} lock_id = {lock.get('lock_id')!r}; want {ID_NAMESPACE!r}")
    sources = {entry.get("axis"): entry for entry in lock.get("sources", [])}
    missing = sorted(set(LOCKED_ARTIFACTS) - set(sources))
    if missing:
        raise SystemExit(f"{lock_path} missing source lock entries for {missing}")
    for axis, relative_path in LOCKED_ARTIFACTS.items():
        verify_locked_artifact(axis, root / relative_path, sources[axis])


def source_lock_entry(root: Path, axis: str) -> dict[str, Any]:
    lock_path = root / SOURCE_LOCK_FILE
    if not lock_path.exists():
        raise SystemExit(f"missing source lock {lock_path}")
    lock = json.loads(lock_path.read_text(encoding="utf-8"))
    for entry in lock.get("sources", []):
        if entry.get("axis") == axis:
            return entry
    raise SystemExit(f"{lock_path} missing source lock entry for {axis}")


def verify_locked_artifact(axis: str, path: Path, entry: dict[str, Any]) -> None:
    if not path.exists():
        raise SystemExit(f"{axis}: missing locked artifact {path}")
    content_length = entry.get("content_length")
    if content_length is not None and path.stat().st_size != int(content_length):
        raise SystemExit(f"{axis}: {path} size {path.stat().st_size}; want {content_length}")
    expected_sha256 = str(entry.get("sha256") or "").strip().lower()
    if not expected_sha256:
        raise SystemExit(f"{axis}: source lock must include sha256")
    checks = [("sha256", expected_sha256.removeprefix("sha256:"))]
    expected_md5 = str(entry.get("md5") or "").strip().lower()
    if expected_md5:
        checks.append(("md5", expected_md5.removeprefix("md5:")))
    for algorithm, expected in checks:
        got = file_digest(path, algorithm)
        if got != expected:
            raise SystemExit(f"{axis}: {algorithm} {got}; want {expected}")


def file_digest(path: Path, algorithm: str) -> str:
    digest = hashlib.new(algorithm)
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def empty_generated() -> dict[str, Any]:
    return {
        "corpus": [],
        "cases": [],
        "qrels": [],
        "answers": [],
        "transforms": [],
        "suite": [],
        "sources": [],
        "axis_counts": {},
    }


def add_beir_axis(out: dict[str, Any], root: Path, seed_id: str, axis_name: str, budget: int, args: argparse.Namespace) -> None:
    axis = BEIR_AXES[axis_name]
    dataset_dir = beir.ensure_beir_dataset(
        axis,
        root / "data" / "beir",
        args.beir_base_url.rstrip("/"),
        allow_download=not args.no_download,
    )
    verify_locked_artifact(axis_name, root / LOCKED_ARTIFACTS[axis_name], source_lock_entry(root, axis_name))
    split, qrels = beir.read_qrels(dataset_dir, axis.split_preferences)
    queries = beir.read_jsonl_map(dataset_dir / "queries.jsonl", "_id")
    selected_qrels, corpus_rows = select_beir_axis_rows(
        axis=axis,
        split=split,
        corpus_path=dataset_dir / "corpus.jsonl",
        qrels=qrels,
        budget=budget,
        max_codepoints=args.max_evidence_codepoints,
    )
    case_buffer = io.StringIO()
    qrel_buffer = io.StringIO()
    answer_buffer = io.StringIO()
    transform_buffer = io.StringIO()
    suite_buffer = io.StringIO()
    case_count = beir.write_axis_cases(
        seed_id=ID_NAMESPACE,
        source_seed_id=ID_NAMESPACE,
        axis=axis,
        split=split,
        queries=queries,
        qrels=selected_qrels,
        corpus_slice="six_axis",
        cases_out=case_buffer,
        qrels_out=qrel_buffer,
        answers_out=answer_buffer,
        transforms_out=transform_buffer,
        suite_out=suite_buffer,
    )
    out["corpus"].extend(corpus_rows)
    out["cases"].extend(read_jsonl_buffer(case_buffer))
    out["qrels"].extend(read_jsonl_buffer(qrel_buffer))
    out["answers"].extend(read_jsonl_buffer(answer_buffer))
    out["transforms"].extend(read_jsonl_buffer(transform_buffer))
    out["suite"].extend(read_jsonl_buffer(suite_buffer))
    out["sources"].append({
        "name": f"BEIR {axis.dataset}",
        "url": f"{args.beir_base_url.rstrip('/')}/{axis.dataset}.zip",
        "license": "See upstream dataset metadata",
        "notes": axis.description,
    })
    out["axis_counts"][axis_name] = {"corpus": len(corpus_rows), "cases": case_count}


def select_beir_axis_rows(
    *,
    axis: Any,
    split: str,
    corpus_path: Path,
    qrels: dict[str, list[tuple[str, float]]],
    budget: int,
    max_codepoints: int,
) -> tuple[dict[str, list[tuple[str, float]]], list[dict[str, Any]]]:
    positive_doc_ids = {docid for refs in qrels.values() for docid, _score in refs}
    positive_rows: dict[str, dict[str, Any]] = {}
    filler_rows: list[dict[str, Any]] = []
    with corpus_path.open("r", encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, start=1):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            docid = str(row.get("_id", "")).strip()
            if not docid:
                raise SystemExit(f"{corpus_path}:{line_number}: missing _id")
            corpus_row = nontruncated_beir_corpus_row(axis, split, docid, row, max_codepoints)
            if corpus_row is None:
                continue
            if docid in positive_doc_ids:
                positive_rows[docid] = corpus_row
            elif len(filler_rows) < budget:
                filler_rows.append(corpus_row)
    selected_qrels: dict[str, list[tuple[str, float]]] = {}
    selected_positive_doc_ids: set[str] = set()
    for qid in sorted(qrels):
        refs = qrels[qid]
        if any(docid not in positive_rows for docid, _score in refs):
            continue
        next_doc_ids = {docid for docid, _score in refs}
        if len(selected_positive_doc_ids | next_doc_ids) > budget:
            continue
        selected_qrels[qid] = refs
        selected_positive_doc_ids.update(next_doc_ids)
        if len(selected_positive_doc_ids) >= budget:
            break
    if not selected_qrels:
        raise SystemExit(f"{axis.axis}: no qrels fit the non-truncated corpus budget")
    rows = [positive_rows[docid] for docid in sorted(selected_positive_doc_ids)]
    used = {row["source_doc_id"] for row in rows}
    for row in filler_rows:
        if len(rows) >= budget:
            break
        if row["source_doc_id"] in used:
            continue
        rows.append(row)
        used.add(row["source_doc_id"])
    if len(rows) != budget:
        raise SystemExit(f"{axis.axis}: corpus rows = {len(rows)}; want {budget}")
    return selected_qrels, rows


def nontruncated_beir_corpus_row(axis: Any, split: str, original_doc_id: str, doc: dict[str, Any], max_codepoints: int) -> dict[str, Any] | None:
    title = str(doc.get("title") or "").strip()
    body = beir.text_field(doc)
    content = f"{title}\n\n{body}".strip() if title else body
    if not content:
        content = title or original_doc_id
    if len(content) > max_codepoints:
        return None
    return {
        "source_doc_id": beir.source_doc_id_for(ID_NAMESPACE, axis.axis, original_doc_id),
        "title": title,
        "content": content,
        "source_dataset": f"beir/{axis.dataset}",
        "source_type": "public_rag_document",
        "authority": "public_qrels",
        "source_quality": 0.75,
        "labels": ["eval", "public_6axis", "six_axis", axis.axis, axis.dataset],
        "metadata": {
            "axis": axis.axis,
            "dataset": axis.dataset,
            "split": split,
            "source_dataset_doc_id": original_doc_id,
        },
    }


def add_semantic_axis(out: dict[str, Any], axis_name: str, budget: int, cases: list[Any]) -> None:
    rows: list[dict[str, Any]] = []
    selected_cases: list[Any] = []
    filler_rows: list[dict[str, Any]] = []
    seen_rows: set[str] = set()
    for case in cases:
        case_rows = [asdict(row) for row in case.corpus_rows]
        if len(rows) + len(case_rows) <= budget:
            selected_cases.append(case)
            rows.extend(case_rows)
            seen_rows.update(row["source_doc_id"] for row in case_rows)
        else:
            for row in case_rows:
                row_id = row["source_doc_id"]
                if row_id not in seen_rows:
                    filler_rows.append(row)
    remaining = budget - len(rows)
    if remaining < 0:
        raise SystemExit(f"{axis_name}: selected too many rows")
    if len(filler_rows) < remaining:
        raise SystemExit(f"{axis_name}: only {len(filler_rows)} filler rows for remaining budget {remaining}")
    rows.extend(filler_rows[:remaining])
    out["corpus"].extend(rows)
    for case in selected_cases:
        out["cases"].append({
            "case_id": case.case_id,
            "query": case.query,
            "task_type": case.task_type,
            "difficulty": case.difficulty,
            "slices": ["public_6axis", axis_name, *case.slices],
            "expected_behavior": case.expected_behavior,
            "limit": case.limit,
        })
        refs = [
            {"type": "source_doc", "source_doc_id": source_doc_id, "grade": 1, "reason": "public benchmark evidence"}
            for source_doc_id in case.required_source_doc_ids
        ]
        out["qrels"].append({"case_id": case.case_id, "required_refs": refs, "required_evidence_refs": refs})
        out["answers"].append({
            "case_id": case.case_id,
            "reference_answer": case.reference_answer,
            "must_include": case.must_include,
            "must_not_include": [],
            "expected_behavior": case.expected_behavior,
            "groundedness_policy": "public_6axis_qrels",
        })
        transform = dict(case.transform)
        transform.update({"case_id": case.case_id, "axis": axis_name, "required_source_doc_ids": case.required_source_doc_ids})
        out["transforms"].append(transform)
        out["suite"].append({"case_id": case.case_id, "weight": 1, "slices": ["public_6axis", axis_name, *case.slices]})
    out["axis_counts"][axis_name] = {"corpus": len(rows), "cases": len(selected_cases)}


def validate_generated(generated: dict[str, Any], expected_size: int) -> None:
    if len(generated["corpus"]) != expected_size:
        raise SystemExit(f"corpus rows = {len(generated['corpus'])}; want {expected_size}")
    seen_doc_ids: set[str] = set()
    seen_content_by_scope: set[tuple[str, str]] = set()
    for index, row in enumerate(generated["corpus"], start=1):
        source_doc_id = str(row.get("source_doc_id", "")).strip()
        content = str(row.get("content", "")).strip()
        scope = str((row.get("metadata") or {}).get("case_id") or row.get("source_dataset") or "")
        if not source_doc_id:
            raise SystemExit(f"corpus row {index} missing source_doc_id")
        if source_doc_id in seen_doc_ids:
            raise SystemExit(f"duplicate source_doc_id {source_doc_id}")
        seen_doc_ids.add(source_doc_id)
        if not content:
            raise SystemExit(f"corpus row {index} missing content")
        if len(content) > semantic.MAX_EVIDENCE_CODEPOINTS:
            raise SystemExit(f"{source_doc_id} has {len(content)} code points")
        content_key = (scope, content)
        if content_key in seen_content_by_scope:
            raise SystemExit(f"duplicate content in scope {scope}: {source_doc_id}")
        seen_content_by_scope.add(content_key)
        metadata = row.get("metadata") or {}
        if metadata.get("truncated"):
            raise SystemExit(f"{source_doc_id} was truncated; split source evidence instead")
        leaked_keys = {"answer", "reference_answer", "qrels", "required_source_doc_ids"}
        if leaked_keys & set(metadata):
            raise SystemExit(f"{source_doc_id} metadata leaks qrel/answer keys")
    case_ids = {case["case_id"] for case in generated["cases"]}
    if len(case_ids) != len(generated["cases"]):
        raise SystemExit("duplicate case_id")
    qrel_ids = {qrel["case_id"] for qrel in generated["qrels"]}
    suite_ids = {case["case_id"] for case in generated["suite"]}
    if suite_ids != case_ids or qrel_ids != case_ids:
        raise SystemExit("suite, cases, and qrels case IDs differ")
    for qrel in generated["qrels"]:
        refs = qrel.get("required_refs") or []
        if not refs:
            raise SystemExit(f"qrel {qrel['case_id']} has no required_refs")
        for ref in refs:
            if ref.get("source_doc_id") not in seen_doc_ids:
                raise SystemExit(f"qrel {qrel['case_id']} missing source_doc_id {ref.get('source_doc_id')}")
    for axis, want in AXIS_BUDGETS[expected_size].items():
        got = generated["axis_counts"].get(axis, {}).get("corpus", 0)
        if got != want:
            raise SystemExit(f"axis {axis} corpus rows = {got}; want {want}")


def write_seed(stage_dir: Path, suite_path: Path, seed_id: str, size: int, generated: dict[str, Any]) -> None:
    counts = {
        "corpus": len(generated["corpus"]),
        "cases": len(generated["cases"]),
        "qrels": len(generated["qrels"]),
        "answers": len(generated["answers"]),
        "transforms": len(generated["transforms"]),
    }
    write_jsonl(stage_dir / "corpus.jsonl", generated["corpus"])
    write_jsonl(stage_dir / "cases.jsonl", generated["cases"])
    write_jsonl(stage_dir / "qrels.jsonl", generated["qrels"])
    write_jsonl(stage_dir / "answers.jsonl", generated["answers"])
    write_jsonl(stage_dir / "transforms.jsonl", generated["transforms"])
    write_jsonl(suite_path, generated["suite"])
    write_licenses(stage_dir / "licenses.md", generated["sources"])
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "seed_id": seed_id,
        "description": f"Validated public six-axis Dense-Mem evaluation seed with {size} corpus rows.",
        "generated_at": "deterministic",
        "corpus_file": "corpus.jsonl",
        "cases_file": "cases.jsonl",
        "qrels_file": "qrels.jsonl",
        "answers_file": "answers.jsonl",
        "transforms_file": "transforms.jsonl",
        "licenses_file": "licenses.md",
        "validation_report_file": "validation_report.json",
        "counts": counts,
        "sources": generated["sources"],
    }
    write_json(stage_dir / "seed_manifest.json", manifest)
    write_json(stage_dir / "public_eval_manifest.json", {
        "seed_id": seed_id,
        "id_namespace": ID_NAMESPACE,
        "axis_counts": generated["axis_counts"],
        "counts": counts,
    })


def write_validation_report(stage_dir: Path, seed_id: str, size: int, seed_hash: str, generated: dict[str, Any]) -> None:
    write_json(stage_dir / "validation_report.json", {
        "schema_version": VALIDATION_SCHEMA_VERSION,
        "seed_id": seed_id,
        "status": "passed",
        "seed_hash": seed_hash,
        "id_namespace": ID_NAMESPACE,
        "expected_corpus_rows": size,
        "axis_counts": generated["axis_counts"],
        "checks": {
            "exact_counts": "passed",
            "qrels_resolve": "passed",
            "max_codepoints": "passed",
            "duplicate_source_doc_ids": "passed",
            "metadata_leakage": "passed",
        },
    })


def stable_seed_hash(seed_dir: Path) -> str:
    digest = hashlib.sha256()
    for name in ["seed_manifest.json", "corpus.jsonl", "cases.jsonl", "qrels.jsonl", "answers.jsonl", "transforms.jsonl", "licenses.md"]:
        path = seed_dir / name
        digest.update(f"file:{name}\n".encode("utf-8"))
        digest.update(path.read_bytes())
        digest.update(b"\n")
    return "sha256:" + digest.hexdigest()


def read_jsonl_buffer(buffer: io.StringIO) -> list[dict[str, Any]]:
	buffer.seek(0)
	return [json.loads(line) for line in buffer.getvalue().splitlines() if line.strip()]


def load_jsonl(path: Path) -> list[dict[str, Any]]:
	return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def write_jsonl(path: Path, rows: list[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as handle:
        for row in rows:
            json.dump(row, handle, ensure_ascii=False, sort_keys=True)
            handle.write("\n")


def write_json(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as handle:
        json.dump(value, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")


def write_licenses(path: Path, sources: list[dict[str, str]]) -> None:
    lines = [
        "# Public Six-Axis Seed Sources",
        "",
        "This seed pack is generated locally from public benchmark datasets.",
        "Do not commit generated seed packs unless upstream licenses have been reviewed for that purpose.",
        "",
    ]
    for source in sources:
        lines.extend([
            f"## {source['name']}",
            "",
            f"- URL: {source.get('url', '')}",
            f"- License: {source.get('license', 'See upstream metadata')}",
            f"- Notes: {source.get('notes', '')}",
            "",
        ])
    path.write_text("\n".join(lines), encoding="utf-8")


if __name__ == "__main__":
    raise SystemExit(main())
