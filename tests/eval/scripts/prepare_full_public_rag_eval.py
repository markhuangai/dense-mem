#!/usr/bin/env python3
"""Prepare a public Dense-Mem RAG eval seed from BEIR-format datasets."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import sys
import tempfile
import time
import urllib.request
import zipfile
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable, TextIO


SCHEMA_VERSION = "dense-mem.eval.seed.v1"
DEFAULT_BEIR_BASE_URL = "https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets"


@dataclass(frozen=True)
class AxisConfig:
    key: str
    axis: str
    dataset: str
    split_preferences: tuple[str, ...]
    task_type: str
    difficulty: str
    description: str
    evidence_qrels: bool = False


AXES = {
    "beir_standard": AxisConfig(
        key="beir_standard",
        axis="beir_standard",
        dataset="scifact",
        split_preferences=("test", "dev", "train"),
        task_type="public_beir_retrieval",
        difficulty="standard",
        description="BEIR SciFact standard retrieval",
    ),
    "msmarco_passage": AxisConfig(
        key="msmarco_passage",
        axis="msmarco_passage",
        dataset="msmarco",
        split_preferences=("dev", "test", "train"),
        task_type="public_msmarco_passage_retrieval",
        difficulty="large_web_passage",
        description="MS MARCO passage retrieval through BEIR packaging",
    ),
    "hotpotqa_multihop": AxisConfig(
        key="hotpotqa_multihop",
        axis="hotpotqa_multihop",
        dataset="hotpotqa",
        split_preferences=("test", "dev", "train"),
        task_type="public_hotpotqa_multihop_retrieval",
        difficulty="multi_hop",
        description="HotpotQA multi-hop retrieval through BEIR packaging",
        evidence_qrels=True,
    ),
}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default="tests/eval", help="eval root directory")
    parser.add_argument("--seed-id", default="public_rag_3axis_5k_v1", help="output seed id")
    parser.add_argument(
        "--source-seed-id",
        default="",
        help="seed id prefix to use for source_doc_id values; defaults to --seed-id",
    )
    parser.add_argument(
        "--max-corpus-docs",
        type=int,
        default=5000,
        help="maximum corpus rows to materialize across selected axes; use 0 for the complete upstream corpus",
    )
    parser.add_argument(
        "--axis",
        action="append",
        choices=sorted(AXES),
        help="axis to include; repeatable; defaults to all axes",
    )
    parser.add_argument(
        "--max-evidence-chars",
        type=int,
        default=900,
        help="maximum characters per imported evidence row; keep below server validation limit",
    )
    parser.add_argument("--beir-base-url", default=DEFAULT_BEIR_BASE_URL)
    parser.add_argument("--force", action="store_true", help="remove an existing output seed first")
    parser.add_argument("--no-download", action="store_true", help="only use already-cached dataset zips")
    args = parser.parse_args()

    if args.max_evidence_chars <= 0:
        parser.error("--max-evidence-chars must be positive")
    if args.max_corpus_docs < 0:
        parser.error("--max-corpus-docs must be non-negative")

    root = Path(args.root)
    data_root = root / "data" / "beir"
    seed_dir = root / "seeds" / args.seed_id
    suite_path = root / "suites" / f"{args.seed_id}.jsonl"
    axes = [AXES[name] for name in (args.axis or sorted(AXES))]
    source_seed_id = args.source_seed_id.strip() or args.seed_id
    limited_corpus = args.max_corpus_docs > 0
    axis_budgets = allocate_axis_budgets(args.max_corpus_docs, len(axes))
    corpus_slice = "full_corpus" if not limited_corpus else "budgeted_corpus"

    if seed_dir.exists():
        if not args.force:
            raise SystemExit(f"{seed_dir} already exists; use --force to rebuild")
        shutil.rmtree(seed_dir)
    seed_dir.mkdir(parents=True, exist_ok=True)
    suite_path.parent.mkdir(parents=True, exist_ok=True)

    counts = {
        "corpus": 0,
        "cases": 0,
        "qrels": 0,
        "answers": 0,
        "transforms": 0,
    }
    sources: list[dict] = []
    axis_summaries: list[dict] = []

    with (
        (seed_dir / "corpus.jsonl").open("w", encoding="utf-8") as corpus_out,
        (seed_dir / "cases.jsonl").open("w", encoding="utf-8") as cases_out,
        (seed_dir / "qrels.jsonl").open("w", encoding="utf-8") as qrels_out,
        (seed_dir / "answers.jsonl").open("w", encoding="utf-8") as answers_out,
        (seed_dir / "transforms.jsonl").open("w", encoding="utf-8") as transforms_out,
        suite_path.open("w", encoding="utf-8") as suite_out,
    ):
        for axis_index, axis in enumerate(axes):
            dataset_dir = ensure_beir_dataset(
                axis,
                data_root,
                args.beir_base_url.rstrip("/"),
                allow_download=not args.no_download,
            )
            split, qrels = read_qrels(dataset_dir, axis.split_preferences)
            queries = read_jsonl_map(dataset_dir / "queries.jsonl", "_id")
            axis_budget = axis_budgets[axis_index]
            selected_qrels, positive_doc_ids = select_axis_qrels(qrels, axis_budget, limited_corpus)
            corpus_count, missing_positive = write_full_axis_corpus(
                source_seed_id=source_seed_id,
                axis=axis,
                split=split,
                corpus_path=dataset_dir / "corpus.jsonl",
                positive_doc_ids=positive_doc_ids,
                max_docs=axis_budget if limited_corpus else 0,
                max_evidence_chars=args.max_evidence_chars,
                corpus_slice=corpus_slice,
                out=corpus_out,
            )
            if missing_positive:
                first = sorted(missing_positive)[0]
                raise SystemExit(
                    f"{axis.key}: missing {len(missing_positive)} qrel documents from corpus; first={first}"
                )
            case_count = write_axis_cases(
                seed_id=args.seed_id,
                source_seed_id=source_seed_id,
                axis=axis,
                split=split,
                queries=queries,
                qrels=selected_qrels,
                corpus_slice=corpus_slice,
                cases_out=cases_out,
                qrels_out=qrels_out,
                answers_out=answers_out,
                transforms_out=transforms_out,
                suite_out=suite_out,
            )
            counts["corpus"] += corpus_count
            counts["cases"] += case_count
            counts["qrels"] += case_count
            counts["answers"] += case_count
            counts["transforms"] += case_count
            axis_summaries.append(
                {
                    "axis": axis.axis,
                    "dataset": axis.dataset,
                    "split": split,
                    "budget": axis_budget,
                    "corpus": corpus_count,
                    "cases": case_count,
                    "positive_doc_ids": len(positive_doc_ids),
                }
            )
            sources.append(
                {
                    "name": f"BEIR {axis.dataset}",
                    "url": f"{args.beir_base_url.rstrip('/')}/{axis.dataset}.zip",
                    "license": "See upstream dataset metadata",
                    "notes": axis.description,
                }
            )
            warn(f"{axis.key}: split={split} corpus={corpus_count} cases={case_count}")

    write_licenses(seed_dir / "licenses.md", sources)
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "seed_id": args.seed_id,
        "description": seed_description(args.max_corpus_docs),
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "corpus_file": "corpus.jsonl",
        "cases_file": "cases.jsonl",
        "qrels_file": "qrels.jsonl",
        "answers_file": "answers.jsonl",
        "transforms_file": "transforms.jsonl",
        "licenses_file": "licenses.md",
        "counts": counts,
        "sources": sources,
    }
    write_json(seed_dir / "seed_manifest.json", manifest)
    write_json(
        seed_dir / "public_eval_manifest.json",
        {
            "seed_id": args.seed_id,
            "source_seed_id": source_seed_id,
            "generated_at": manifest["generated_at"],
            "max_corpus_docs": args.max_corpus_docs,
            "max_evidence_chars": args.max_evidence_chars,
            "axes": axis_summaries,
            "counts": counts,
        },
    )
    print(f"wrote {seed_dir}")
    print(f"wrote {suite_path}")
    return 0


def seed_description(max_corpus_docs: int) -> str:
    if max_corpus_docs == 0:
        return "Full public 3-axis RAG retrieval seed: BEIR standard, MS MARCO passage, HotpotQA multi-hop."
    return (
        f"Budgeted public 3-axis RAG retrieval seed with about {max_corpus_docs} corpus rows: "
        "BEIR standard, MS MARCO passage, HotpotQA multi-hop."
    )


def allocate_axis_budgets(max_corpus_docs: int, axis_count: int) -> list[int]:
    if axis_count <= 0:
        return []
    if max_corpus_docs == 0:
        return [0] * axis_count
    base = max_corpus_docs // axis_count
    remainder = max_corpus_docs % axis_count
    return [base + (1 if index < remainder else 0) for index in range(axis_count)]


def select_axis_qrels(
    qrels: dict[str, list[tuple[str, float]]],
    corpus_budget: int,
    limited_corpus: bool,
) -> tuple[dict[str, list[tuple[str, float]]], set[str]]:
    if not limited_corpus:
        return qrels, {docid for refs in qrels.values() for docid, _score in refs}
    if corpus_budget <= 0:
        raise SystemExit("corpus budget is too small to allocate at least one row per axis")
    selected: dict[str, list[tuple[str, float]]] = {}
    positive_doc_ids: set[str] = set()
    for qid in sorted(qrels):
        refs = qrels[qid]
        next_doc_ids = {docid for docid, _score in refs}
        if len(positive_doc_ids | next_doc_ids) > corpus_budget:
            continue
        selected[qid] = refs
        positive_doc_ids.update(next_doc_ids)
        if len(positive_doc_ids) >= corpus_budget:
            break
    if not selected:
        raise SystemExit(f"corpus budget {corpus_budget} is too small to include any qrels-backed cases")
    return selected, positive_doc_ids


def ensure_beir_dataset(
    axis: AxisConfig,
    data_root: Path,
    base_url: str,
    allow_download: bool,
) -> Path:
    data_root.mkdir(parents=True, exist_ok=True)
    zip_path = data_root / f"{axis.dataset}.zip"
    extract_root = data_root / axis.dataset
    corpus_path = find_dataset_file(extract_root, "corpus.jsonl")
    if corpus_path is not None:
        return corpus_path.parent
    if not zip_path.exists():
        if not allow_download:
            raise SystemExit(f"missing {zip_path} and --no-download was set")
        download_file(f"{base_url}/{axis.dataset}.zip", zip_path)
    extract_zip(zip_path, extract_root)
    corpus_path = find_dataset_file(extract_root, "corpus.jsonl")
    if corpus_path is None:
        raise SystemExit(f"{zip_path} did not contain corpus.jsonl")
    if find_dataset_file(extract_root, "queries.jsonl") is None:
        raise SystemExit(f"{zip_path} did not contain queries.jsonl")
    return corpus_path.parent


def download_file(url: str, path: Path) -> None:
    warn(f"downloading {url}")
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as tmp:
        tmp_path = Path(tmp.name)
    try:
        with urllib.request.urlopen(url) as response, tmp_path.open("wb") as out:
            shutil.copyfileobj(response, out, length=1024 * 1024)
        os.replace(tmp_path, path)
    except Exception:
        tmp_path.unlink(missing_ok=True)
        raise


def extract_zip(zip_path: Path, extract_root: Path) -> None:
    warn(f"extracting {zip_path}")
    if extract_root.exists():
        shutil.rmtree(extract_root)
    extract_root.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(zip_path) as zf:
        zf.extractall(extract_root)


def find_dataset_file(root: Path, filename: str) -> Path | None:
    if not root.exists():
        return None
    direct = root / filename
    if direct.exists():
        return direct
    matches = sorted(root.rglob(filename))
    return matches[0] if matches else None


def read_jsonl_map(path: Path, key: str) -> dict[str, dict]:
    rows: dict[str, dict] = {}
    with path.open("r", encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, start=1):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            row_id = str(row.get(key, "")).strip()
            if not row_id:
                raise SystemExit(f"{path}:{line_number}: missing {key}")
            rows[row_id] = row
    return rows


def read_qrels(dataset_dir: Path, split_preferences: Iterable[str]) -> tuple[str, dict[str, list[tuple[str, float]]]]:
    qrels_dir = dataset_dir / "qrels"
    if not qrels_dir.exists():
        raise SystemExit(f"{dataset_dir} missing qrels directory")
    for split in split_preferences:
        path = qrels_dir / f"{split}.tsv"
        if path.exists():
            return split, parse_qrels(path)
    available = ", ".join(sorted(p.name for p in qrels_dir.glob("*.tsv")))
    wanted = ", ".join(split_preferences)
    raise SystemExit(f"{dataset_dir} has qrels [{available}], none of requested [{wanted}]")


def parse_qrels(path: Path) -> dict[str, list[tuple[str, float]]]:
    qrels: dict[str, list[tuple[str, float]]] = {}
    with path.open("r", encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, start=1):
            line = line.rstrip("\n")
            if not line:
                continue
            parts = line.split("\t")
            if line_number == 1 and any("query" in part.lower() or "corpus" in part.lower() for part in parts):
                continue
            qid, docid, score = parse_qrel_parts(parts, path, line_number)
            if score <= 0:
                continue
            qrels.setdefault(qid, []).append((docid, score))
    for qid in list(qrels):
        qrels[qid].sort(key=lambda item: (-item[1], item[0]))
    return qrels


def parse_qrel_parts(parts: list[str], path: Path, line_number: int) -> tuple[str, str, float]:
    if len(parts) >= 4 and parts[1] in {"0", "Q0"}:
        qid, docid, score_text = parts[0], parts[2], parts[3]
    elif len(parts) >= 3:
        qid, docid, score_text = parts[0], parts[1], parts[2]
    else:
        raise SystemExit(f"{path}:{line_number}: malformed qrel row")
    try:
        score = float(score_text)
    except ValueError as exc:
        raise SystemExit(f"{path}:{line_number}: malformed qrel score {score_text!r}") from exc
    return qid.strip(), docid.strip(), score


def write_full_axis_corpus(
    *,
    source_seed_id: str,
    axis: AxisConfig,
    split: str,
    corpus_path: Path,
    positive_doc_ids: set[str],
    max_docs: int,
    max_evidence_chars: int,
    corpus_slice: str,
    out: TextIO,
) -> tuple[int, set[str]]:
    seen_positive = set()
    remaining_positive = set(positive_doc_ids)
    count = 0
    with corpus_path.open("r", encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, start=1):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            docid = str(row.get("_id", "")).strip()
            if not docid:
                raise SystemExit(f"{corpus_path}:{line_number}: missing _id")
            is_positive = docid in positive_doc_ids
            if max_docs > 0:
                if is_positive:
                    remaining_positive.discard(docid)
                elif count + len(remaining_positive) >= max_docs:
                    continue
            if is_positive:
                seen_positive.add(docid)
            write_jsonl_row(
                out,
                corpus_row(
                    axis=axis,
                    split=split,
                    source_doc_id=source_doc_id_for(source_seed_id, axis.axis, docid),
                    original_doc_id=docid,
                    doc=row,
                    max_evidence_chars=max_evidence_chars,
                    corpus_slice=corpus_slice,
                ),
            )
            count += 1
    return count, positive_doc_ids - seen_positive


def write_axis_cases(
    *,
    seed_id: str,
    source_seed_id: str,
    axis: AxisConfig,
    split: str,
    queries: dict[str, dict],
    qrels: dict[str, list[tuple[str, float]]],
    corpus_slice: str,
    cases_out: TextIO,
    qrels_out: TextIO,
    answers_out: TextIO,
    transforms_out: TextIO,
    suite_out: TextIO,
) -> int:
    count = 0
    for qid in sorted(qrels):
        if qid not in queries:
            warn(f"{axis.key}: skipping qrel query {qid!r}; missing query text")
            continue
        refs = qrels[qid]
        if not refs:
            continue
        case_id = case_id_for(seed_id, axis.axis, qid)
        required_refs = [
            {
                "type": "source_doc",
                "source_doc_id": source_doc_id_for(source_seed_id, axis.axis, docid),
                "grade": score,
                "reason": "public qrels positive",
            }
            for docid, score in refs
        ]
        qrel = {"case_id": case_id, "required_refs": required_refs}
        if axis.evidence_qrels:
            qrel["required_evidence_refs"] = required_refs
        case = {
            "case_id": case_id,
            "query": text_field(queries[qid]),
            "task_type": axis.task_type,
            "difficulty": axis.difficulty,
            "slices": ["public_rag", corpus_slice, axis.axis, axis.dataset, split],
            "expected_behavior": f"retrieve public qrels-positive source documents from the {corpus_slice.replace('_', ' ')}",
            "limit": 10,
            "include_dreams": False,
            "use_communities": True,
        }
        write_jsonl_row(cases_out, case)
        write_jsonl_row(qrels_out, qrel)
        write_jsonl_row(
            answers_out,
            {
                "case_id": case_id,
                "reference_answer": "",
                "must_include": [],
                "must_not_include": [],
                "expected_behavior": "rank qrels-positive documents in returned refs/context",
                "groundedness_policy": "public_qrels",
            },
        )
        write_jsonl_row(
            transforms_out,
            {
                "case_id": case_id,
                "axis": axis.axis,
                "dataset": axis.dataset,
                "split": split,
                "query_id": qid,
                "positive_doc_ids": [docid for docid, _score in refs],
                "source_seed_id": source_seed_id,
                "transform": f"{corpus_slice}_beir_qrels_to_dense_mem_seed",
            },
        )
        write_jsonl_row(suite_out, {"case_id": case_id, "weight": 1, "slices": case["slices"]})
        count += 1
    return count


def corpus_row(
    *,
    axis: AxisConfig,
    split: str,
    source_doc_id: str,
    original_doc_id: str,
    doc: dict,
    max_evidence_chars: int,
    corpus_slice: str,
) -> dict:
    title = str(doc.get("title") or "").strip()
    body = text_field(doc)
    content = f"{title}\n\n{body}".strip() if title else body
    truncated = len(content) > max_evidence_chars
    if truncated:
        content = content[:max_evidence_chars].rstrip()
    if not content:
        content = title or original_doc_id
    return {
        "source_doc_id": source_doc_id,
        "title": title,
        "content": content,
        "source_dataset": f"beir/{axis.dataset}",
        "source_type": "public_rag_document",
        "authority": "public_qrels",
        "source_quality": 0.75,
        "labels": ["eval", "public_rag", corpus_slice, axis.axis, axis.dataset],
        "metadata": {
            "axis": axis.axis,
            "dataset": axis.dataset,
            "split": split,
            "source_dataset_doc_id": original_doc_id,
            "truncated": truncated,
        },
    }


def text_field(row: dict) -> str:
    value = row.get("text")
    if value is None:
        value = row.get("query", "")
    if isinstance(value, list):
        return " ".join(str(part) for part in value).strip()
    return str(value or "").strip()


def case_id_for(seed_id: str, axis: str, qid: str) -> str:
    return sanitize_id(f"{seed_id}:{axis}:case:{qid}")


def source_doc_id_for(seed_id: str, axis: str, docid: str) -> str:
    return sanitize_id(f"{seed_id}:{axis}:doc:{docid}")


def sanitize_id(value: str) -> str:
    value = re.sub(r"[^A-Za-z0-9._:-]+", "_", value.strip())
    value = re.sub(r"_+", "_", value).strip("_")
    if len(value) <= 220:
        return value
    digest = hashlib.sha1(value.encode("utf-8")).hexdigest()[:16]
    return f"{value[:200]}:{digest}"


def write_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as handle:
        json.dump(value, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")


def write_jsonl_row(handle: TextIO, row: dict) -> None:
    json.dump(row, handle, ensure_ascii=False, sort_keys=True)
    handle.write("\n")


def write_licenses(path: Path, sources: list[dict]) -> None:
    lines = [
        "# Public RAG Seed Sources",
        "",
        "This full seed pack was generated locally from public retrieval datasets.",
        "Do not commit generated seed packs unless upstream licenses have been reviewed for that purpose.",
        "",
    ]
    for source in sources:
        lines.extend(
            [
                f"## {source['name']}",
                "",
                f"- URL: {source['url']}",
                f"- License: {source['license']}",
                f"- Notes: {source['notes']}",
                "",
            ]
        )
    path.write_text("\n".join(lines), encoding="utf-8")


def warn(message: str) -> None:
    print(message, file=sys.stderr, flush=True)


if __name__ == "__main__":
    raise SystemExit(main())
