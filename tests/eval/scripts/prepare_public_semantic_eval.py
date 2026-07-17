#!/usr/bin/env python3
"""Prepare the Dense-Mem public semantic 3-axis evaluation seed."""

from __future__ import annotations

import argparse
import hashlib
import html.parser
import json
import os
import re
import shutil
import tarfile
import time
import urllib.parse
import urllib.request
import zipfile
from collections import Counter, defaultdict
from dataclasses import dataclass
from http.cookiejar import CookieJar
from pathlib import Path
from typing import Any, Iterable, TextIO


SCHEMA_VERSION = "dense-mem.eval.seed.v1"
DEFAULT_SEED_ID = "public_semantic_3axis_1k_v1"
MAX_EVIDENCE_CODEPOINTS = 999

MUSIQUE_DRIVE_ID = "1tGdADlNjWFaHLeZZGShh2IRcpO6Lv24h"
MUSIQUE_SHA256 = "98f839bf2fd5319f5c688aed77901a6d5c30b3b9f9f691ab9a8ecafb045ee0cd"
QASPER_TRAIN_DEV_URL = "https://qasper-dataset.s3.us-west-2.amazonaws.com/qasper-train-dev-v0.3.tgz"
QASPER_SHA256 = "a28fdf966db827bcee3d873107d6b6669864fb7ca8fbf73a192f5e39191bdb5a"
LONGMEMEVAL_S_REVISION = "98d7416c24c778c2fee6e6f3006e7a073259d48f"
LONGMEMEVAL_S_URL = f"https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/{LONGMEMEVAL_S_REVISION}/longmemeval_s_cleaned.json"
LONGMEMEVAL_S_SHA256 = "d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442"

MUSIQUE_QUOTAS = {2: 150, 3: 150, 4: 150}
QASPER_QUOTAS = {
    ("extractive", "single"): 130,
    ("extractive", "multi"): 130,
    ("freeform", "single"): 25,
    ("freeform", "multi"): 25,
    ("yesno", "single"): 15,
    ("yesno", "multi"): 15,
    ("mixed", "single"): 45,
    ("mixed", "multi"): 45,
}
LONGMEMEVAL_QUOTAS = {
    "single-session-user": 20,
    "single-session-assistant": 20,
    "single-session-preference": 20,
    "temporal-reasoning": 20,
    "knowledge-update": 20,
    "multi-session": 20,
}

DETERMINISTIC_GENERATED_AT = "1970-01-01T00:00:00Z"


@dataclass
class SeedRow:
    source_doc_id: str
    title: str
    content: str
    source_dataset: str
    source_type: str
    authority: str
    source_quality: float
    labels: list[str]
    metadata: dict[str, Any]


@dataclass
class SeedCase:
    case_id: str
    query: str
    task_type: str
    difficulty: str
    slices: list[str]
    expected_behavior: str
    limit: int
    reference_answer: str
    must_include: list[str]
    required_source_doc_ids: list[str]
    corpus_rows: list[SeedRow]
    transform: dict[str, Any]


class GoogleDriveFormParser(html.parser.HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.in_download_form = False
        self.action = ""
        self.inputs: dict[str, str] = {}

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attr = {key: value or "" for key, value in attrs}
        if tag == "form" and attr.get("id") == "download-form":
            self.in_download_form = True
            self.action = attr.get("action", "")
            return
        if self.in_download_form and tag == "input":
            name = attr.get("name", "")
            if name:
                self.inputs[name] = attr.get("value", "")

    def handle_endtag(self, tag: str) -> None:
        if tag == "form":
            self.in_download_form = False


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default="tests/eval", help="eval root directory")
    parser.add_argument("--seed-id", default=DEFAULT_SEED_ID)
    parser.add_argument("--force", action="store_true", help="remove an existing output seed first")
    parser.add_argument("--no-download", action="store_true", help="use only already-cached upstream files")
    parser.add_argument("--max-evidence-codepoints", type=int, default=MAX_EVIDENCE_CODEPOINTS)
    parser.add_argument("--musique-distractors", type=int, default=4)
    parser.add_argument("--qasper-distractors", type=int, default=4)
    parser.add_argument("--longmem-distractor-sessions", type=int, default=4)
    parser.add_argument("--preflight", action="store_true", help="write a 3-case preflight seed instead of the 1,000-case seed")
    args = parser.parse_args()

    if args.max_evidence_codepoints != MAX_EVIDENCE_CODEPOINTS:
        parser.error("this evaluation seed must use max evidence length 999")

    root = Path(args.root)
    data_root = root / "data" / "public_semantic"
    seed_dir = root / "seeds" / args.seed_id
    suite_path = root / "suites" / f"{args.seed_id}.jsonl"
    stage_dir = root / "seeds" / f".{args.seed_id}.staging"
    stage_suite = root / "suites" / f".{args.seed_id}.jsonl.staging"
    install_tx_dir = root / "seeds" / f".{args.seed_id}.install-tx"

    recover_install_transaction(seed_dir, suite_path, install_tx_dir)
    if seed_dir.exists() and not args.force:
        raise SystemExit(f"{seed_dir} already exists; use --force to rebuild")
    remove_path(stage_dir)
    remove_path(stage_suite)
    stage_dir.mkdir(parents=True, exist_ok=True)
    stage_suite.parent.mkdir(parents=True, exist_ok=True)

    ensure_sources(data_root, allow_download=not args.no_download)
    cases = build_cases(
        data_root=data_root,
        seed_id=args.seed_id,
        max_codepoints=args.max_evidence_codepoints,
        musique_distractors=args.musique_distractors,
        qasper_distractors=args.qasper_distractors,
        longmem_distractor_sessions=args.longmem_distractor_sessions,
        preflight=args.preflight,
    )
    validate_cases(cases, expected_cases=3 if args.preflight else 1000)
    write_seed(stage_dir, stage_suite, args.seed_id, cases, args)
    install_generated_seed(stage_dir, stage_suite, seed_dir, suite_path, install_tx_dir)
    print(f"wrote {seed_dir}")
    print(f"wrote {suite_path}")
    return 0


def install_generated_seed(stage_dir: Path, stage_suite: Path, seed_dir: Path, suite_path: Path, tx_dir: Path) -> None:
    recover_install_transaction(seed_dir, suite_path, tx_dir)
    remove_path(tx_dir)
    tx_dir.mkdir(parents=True)
    write_json(tx_dir / "manifest.json", {
        "seed_dir": str(seed_dir),
        "suite_path": str(suite_path),
    })
    seed_backup = tx_dir / "seed_dir.backup"
    suite_backup = tx_dir / "suite_path.backup"
    try:
        if seed_dir.exists():
            os.replace(seed_dir, seed_backup)
        if suite_path.exists():
            os.replace(suite_path, suite_backup)
        os.replace(stage_dir, seed_dir)
        os.replace(stage_suite, suite_path)
    except BaseException:
        recover_install_transaction(seed_dir, suite_path, tx_dir)
        raise
    else:
        shutil.rmtree(tx_dir)


def recover_install_transaction(seed_dir: Path, suite_path: Path, tx_dir: Path) -> None:
    if not tx_dir.exists():
        return
    manifest_path = tx_dir / "manifest.json"
    if not manifest_path.exists():
        shutil.rmtree(tx_dir)
        return
    seed_backup = tx_dir / "seed_dir.backup"
    suite_backup = tx_dir / "suite_path.backup"
    if seed_backup.exists():
        remove_path(seed_dir)
        seed_dir.parent.mkdir(parents=True, exist_ok=True)
        os.replace(seed_backup, seed_dir)
    if suite_backup.exists():
        remove_path(suite_path)
        suite_path.parent.mkdir(parents=True, exist_ok=True)
        os.replace(suite_backup, suite_path)
    shutil.rmtree(tx_dir)


def remove_path(path: Path) -> None:
    if path.is_dir() and not path.is_symlink():
        shutil.rmtree(path)
    elif path.exists():
        path.unlink()


def ensure_sources(data_root: Path, allow_download: bool) -> None:
    data_root.mkdir(parents=True, exist_ok=True)
    sources = [
        (data_root / "qasper-train-dev-v0.3.tgz", QASPER_TRAIN_DEV_URL, QASPER_SHA256),
        (data_root / "longmemeval_s_cleaned.json", LONGMEMEVAL_S_URL, LONGMEMEVAL_S_SHA256),
    ]
    for path, url, sha256 in sources:
        if path.exists():
            verify_sha256(path, sha256)
            continue
        if not allow_download:
            raise SystemExit(f"missing {path} and --no-download was set")
        download_file(url, path)
        verify_sha256(path, sha256)
    musique_zip = data_root / "musique_v1.0.zip"
    if not musique_zip.exists():
        if not allow_download:
            raise SystemExit(f"missing {musique_zip} and --no-download was set")
        download_google_drive(MUSIQUE_DRIVE_ID, musique_zip)
    verify_sha256(musique_zip, MUSIQUE_SHA256)


def verify_sha256(path: Path, expected: str) -> None:
    got = file_sha256(path)
    if got != expected:
        raise SystemExit(f"{path} sha256 {got}; want {expected}")


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def download_file(url: str, path: Path) -> None:
    warn(f"downloading {url}")
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    try:
        with urllib.request.urlopen(url, timeout=120) as response, tmp.open("wb") as out:
            while True:
                chunk = response.read(1024 * 1024)
                if not chunk:
                    break
                out.write(chunk)
        tmp.replace(path)
    except Exception:
        tmp.unlink(missing_ok=True)
        raise


def download_google_drive(file_id: str, path: Path) -> None:
    warn(f"downloading Google Drive file {file_id}")
    cookies = CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
    first_url = "https://drive.google.com/uc?" + urllib.parse.urlencode({"export": "download", "id": file_id})
    with opener.open(first_url, timeout=120) as response:
        body = response.read()
    parser = GoogleDriveFormParser()
    parser.feed(body.decode("utf-8", "ignore"))
    if not parser.action or not parser.inputs:
        raise SystemExit("Google Drive download form not found")
    download_url = parser.action + "?" + urllib.parse.urlencode(parser.inputs)
    tmp = path.with_suffix(path.suffix + ".tmp")
    try:
        with opener.open(download_url, timeout=120) as response, tmp.open("wb") as out:
            while True:
                chunk = response.read(1024 * 1024)
                if not chunk:
                    break
                out.write(chunk)
        tmp.replace(path)
    except Exception:
        tmp.unlink(missing_ok=True)
        raise


def build_cases(
    *,
    data_root: Path,
    seed_id: str,
    max_codepoints: int,
    musique_distractors: int,
    qasper_distractors: int,
    longmem_distractor_sessions: int,
    preflight: bool,
) -> list[SeedCase]:
    if preflight:
        return [
            build_musique_cases(data_root, seed_id, {2: 1}, max_codepoints, musique_distractors)[0],
            build_qasper_cases(data_root, seed_id, {("extractive", "single"): 1}, max_codepoints, qasper_distractors)[0],
            build_longmem_cases(data_root, seed_id, {"single-session-user": 1}, max_codepoints, longmem_distractor_sessions)[0],
        ]
    cases: list[SeedCase] = []
    cases.extend(build_musique_cases(data_root, seed_id, MUSIQUE_QUOTAS, max_codepoints, musique_distractors))
    cases.extend(build_qasper_cases(data_root, seed_id, QASPER_QUOTAS, max_codepoints, qasper_distractors))
    cases.extend(build_longmem_cases(data_root, seed_id, LONGMEMEVAL_QUOTAS, max_codepoints, longmem_distractor_sessions))
    return cases


def build_musique_cases(data_root: Path, seed_id: str, quotas: dict[int, int], max_codepoints: int, distractor_count: int) -> list[SeedCase]:
    selected: list[SeedCase] = []
    counts = Counter()
    with zipfile.ZipFile(data_root / "musique_v1.0.zip") as zf, zf.open("data/musique_ans_v1.0_dev.jsonl") as handle:
        for raw in handle:
            row = json.loads(raw)
            hop_count = len(row.get("question_decomposition") or [])
            if counts[hop_count] >= quotas.get(hop_count, 0):
                continue
            case = musique_case(seed_id, row, max_codepoints, distractor_count)
            if case is None:
                continue
            selected.append(case)
            counts[hop_count] += 1
            if all(counts[key] >= value for key, value in quotas.items()):
                break
    require_quotas("MuSiQue", counts, quotas)
    return selected


def musique_case(seed_id: str, row: dict[str, Any], max_codepoints: int, distractor_count: int) -> SeedCase | None:
    hop_count = len(row.get("question_decomposition") or [])
    raw_case_id = str(row["id"])
    case_id = sanitize_id(f"{seed_id}:musique:{raw_case_id}")
    rows: list[SeedRow] = []
    required: list[str] = []
    support = [p for p in row["paragraphs"] if p.get("is_supporting")]
    distractors = [p for p in row["paragraphs"] if not p.get("is_supporting")]
    for paragraph in support:
        content = f"{paragraph.get('title', '').strip()}\n{paragraph.get('paragraph_text', '').strip()}".strip()
        if len(content) > max_codepoints:
            return None
        source_doc_id = sanitize_id(f"{case_id}:support:{paragraph['idx']}")
        rows.append(seed_row(source_doc_id, paragraph.get("title", ""), content, "musique", "musique_paragraph", case_id, True, {
            "dataset": "musique",
            "raw_id": raw_case_id,
            "hop_count": hop_count,
            "paragraph_idx": paragraph["idx"],
        }))
        required.append(source_doc_id)
    kept_distractors = 0
    for paragraph in distractors:
        if kept_distractors >= distractor_count:
            break
        content = f"{paragraph.get('title', '').strip()}\n{paragraph.get('paragraph_text', '').strip()}".strip()
        if not content or len(content) > max_codepoints:
            continue
        source_doc_id = sanitize_id(f"{case_id}:distractor:{paragraph['idx']}")
        rows.append(seed_row(source_doc_id, paragraph.get("title", ""), content, "musique", "musique_paragraph", case_id, False, {
            "dataset": "musique",
            "raw_id": raw_case_id,
            "hop_count": hop_count,
            "paragraph_idx": paragraph["idx"],
        }))
        kept_distractors += 1
    if not required or len(rows) > 20:
        return None
    answer = clean_text(str(row.get("answer", "")))
    return SeedCase(
        case_id=case_id,
        query=clean_text(row["question"]),
        task_type="public_musique_multihop",
        difficulty=f"{hop_count}_hop",
        slices=["public_semantic", "musique", f"{hop_count}_hop"],
        expected_behavior="retrieve all MuSiQue supporting paragraphs needed for the composed answer",
        limit=10,
        reference_answer=answer,
        must_include=[answer] if answer else [],
        required_source_doc_ids=required,
        corpus_rows=rows,
        transform={"dataset": "musique", "raw_id": raw_case_id, "hop_count": hop_count, "support_count": len(required), "distractor_count": kept_distractors},
    )


def build_qasper_cases(data_root: Path, seed_id: str, quotas: dict[tuple[str, str], int], max_codepoints: int, distractor_count: int) -> list[SeedCase]:
	selected: list[SeedCase] = []
	counts = Counter()
	with tarfile.open(data_root / "qasper-train-dev-v0.3.tgz") as tf:
		split_papers = [
			("dev", json.load(tf.extractfile("qasper-dev-v0.3.json"))),
			("train", json.load(tf.extractfile("qasper-train-v0.3.json"))),
		]
	for split, papers in split_papers:
		for paper_id in sorted(papers):
			paper = papers[paper_id]
			for qa in sorted(paper.get("qas", []), key=lambda item: item.get("question_id", "")):
				candidate = classify_qasper_qa(qa)
				if candidate is None:
					continue
				key = (candidate["kind"], candidate["scope"])
				if counts[key] >= quotas.get(key, 0):
					continue
				case = qasper_case(seed_id, split, paper_id, paper, qa, candidate, max_codepoints, distractor_count)
				if case is None:
					continue
				selected.append(case)
				counts[key] += 1
				if all(counts[key] >= value for key, value in quotas.items()):
					require_quotas("QASPER", counts, quotas)
					return selected
	require_quotas("QASPER", counts, quotas)
	return selected


def classify_qasper_qa(qa: dict[str, Any]) -> dict[str, Any] | None:
    candidates = []
    for wrapper in qa.get("answers") or []:
        answer = wrapper.get("answer") or {}
        kind = qasper_answer_kind(answer)
        if kind in {"unanswerable", "unknown"}:
            continue
        evidence = unique_texts(answer.get("highlighted_evidence") or answer.get("evidence") or [])
        if not evidence:
            continue
        candidates.append({"kind": kind, "scope": "multi" if len(evidence) > 1 else "single", "answer": answer, "evidence": evidence})
    if not candidates:
        return None
    kinds = {item["kind"] for item in candidates}
    if len(kinds) > 1:
        evidence: list[str] = []
        answers: list[str] = []
        for item in candidates:
            evidence.extend(item["evidence"])
            answer_text = qasper_reference_answer(item["answer"])
            if answer_text:
                answers.append(answer_text)
        evidence = unique_texts(evidence)
        return {
            "kind": "mixed",
            "scope": "multi" if any(item["scope"] == "multi" for item in candidates) else "single",
            "answer_text": "; ".join(unique_texts(answers)),
            "evidence": evidence,
        }
    first = candidates[0]
    return {
        "kind": first["kind"],
        "scope": first["scope"],
        "answer_text": qasper_reference_answer(first["answer"]),
        "evidence": first["evidence"],
    }


def qasper_answer_kind(answer: dict[str, Any]) -> str:
    if answer.get("unanswerable"):
        return "unanswerable"
    parts = []
    if answer.get("extractive_spans"):
        parts.append("extractive")
    if clean_text(answer.get("free_form_answer", "")):
        parts.append("freeform")
    if answer.get("yes_no") is not None:
        parts.append("yesno")
    if len(parts) > 1:
        return "mixed"
    return parts[0] if parts else "unknown"


def qasper_reference_answer(answer: dict[str, Any]) -> str:
    if answer.get("yes_no") is not None:
        return "yes" if answer.get("yes_no") else "no"
    if answer.get("extractive_spans"):
        return "; ".join(unique_texts(answer.get("extractive_spans") or []))
    return clean_text(answer.get("free_form_answer", ""))


def qasper_case(
    seed_id: str,
    split: str,
    paper_id: str,
    paper: dict[str, Any],
    qa: dict[str, Any],
    candidate: dict[str, Any],
    max_codepoints: int,
    distractor_count: int,
) -> SeedCase | None:
    case_id = sanitize_id(f"{seed_id}:qasper:{split}:{paper_id}:{qa['question_id']}")
    rows: list[SeedRow] = []
    required: list[str] = []
    for index, evidence in enumerate(candidate["evidence"]):
        for chunk_index, chunk in enumerate(split_with_prefix(f"QASPER evidence. Paper: {paper.get('title', paper_id)}.", evidence, max_codepoints)):
            source_doc_id = sanitize_id(f"{case_id}:gold:{index}:{chunk_index}")
            rows.append(seed_row(source_doc_id, paper.get("title", ""), chunk, "qasper", "qasper_evidence", case_id, True, {
                "dataset": "qasper",
                "paper_id": paper_id,
                "split": split,
                "question_id": qa["question_id"],
                "answer_kind": candidate["kind"],
                "evidence_scope": candidate["scope"],
                "evidence_index": index,
                "chunk_index": chunk_index,
            }))
            required.append(source_doc_id)
    if not required or len(required) > 10:
        return None
    distractors = qasper_distractor_paragraphs(paper, candidate["evidence"], distractor_count)
    for index, item in enumerate(distractors):
        for chunk_index, chunk in enumerate(split_with_prefix(f"QASPER paper context. Section: {item['section']}.", item["text"], max_codepoints)):
            source_doc_id = sanitize_id(f"{case_id}:distractor:{index}:{chunk_index}")
            rows.append(seed_row(source_doc_id, paper.get("title", ""), chunk, "qasper", "qasper_paper_context", case_id, False, {
                "dataset": "qasper",
                "paper_id": paper_id,
                "split": split,
                "question_id": qa["question_id"],
                "section": item["section"],
                "paragraph_index": item["paragraph_index"],
                "chunk_index": chunk_index,
            }))
    return SeedCase(
        case_id=case_id,
        query=clean_text(qa["question"]),
        task_type=f"public_qasper_{candidate['kind']}",
        difficulty=candidate["scope"],
        slices=["public_semantic", "qasper", candidate["kind"], candidate["scope"]],
        expected_behavior="retrieve QASPER evidence snippets that support the answer",
        limit=10,
        reference_answer=candidate["answer_text"],
        must_include=unique_texts([candidate["answer_text"]]) if candidate["answer_text"] else [],
        required_source_doc_ids=required,
        corpus_rows=rows,
		transform={"dataset": "qasper", "split": split, "paper_id": paper_id, "question_id": qa["question_id"], "answer_kind": candidate["kind"], "evidence_scope": candidate["scope"], "required_chunks": len(required), "distractor_count": len(distractors)},
	)


def qasper_distractor_paragraphs(paper: dict[str, Any], gold_evidence: list[str], limit: int) -> list[dict[str, Any]]:
    out = []
    gold_texts = [clean_text(item).lower() for item in gold_evidence if clean_text(item)]
    for section in paper.get("full_text") or []:
        section_name = clean_text(section.get("section_name", ""))
        for index, paragraph in enumerate(section.get("paragraphs") or []):
            text = clean_text(paragraph)
            text_lc = text.lower()
            if not text or any(gold in text_lc for gold in gold_texts):
                continue
            out.append({"section": section_name, "paragraph_index": index, "text": text})
            if len(out) >= limit:
                return out
    return out


def build_longmem_cases(data_root: Path, seed_id: str, quotas: dict[str, int], max_codepoints: int, distractor_sessions: int) -> list[SeedCase]:
    rows = json.load((data_root / "longmemeval_s_cleaned.json").open("r", encoding="utf-8"))
    selected: list[SeedCase] = []
    counts = Counter()
    for row in sorted(rows, key=lambda item: (item["question_type"], item["question_id"])):
        qtype = row["question_type"]
        if row["question_id"].endswith("_abs"):
            continue
        if counts[qtype] >= quotas.get(qtype, 0):
            continue
        case = longmem_case(seed_id, row, max_codepoints, distractor_sessions)
        if case is None:
            continue
        selected.append(case)
        counts[qtype] += 1
        if all(counts[key] >= value for key, value in quotas.items()):
            require_quotas("LongMemEval-S", counts, quotas)
            return selected
    require_quotas("LongMemEval-S", counts, quotas)
    return selected


def longmem_case(seed_id: str, row: dict[str, Any], max_codepoints: int, distractor_sessions: int) -> SeedCase | None:
    raw_id = row["question_id"]
    case_id = sanitize_id(f"{seed_id}:longmemeval_s:{raw_id}")
    answer_sessions = set(row.get("answer_session_ids") or [])
    selected_sessions = set(answer_sessions)
    for session_index in longmem_distractor_indices(row, distractor_sessions):
        selected_sessions.add(row["haystack_session_ids"][session_index])
    rows: list[SeedRow] = []
    required: list[str] = []
    for session_index, session_id in enumerate(row["haystack_session_ids"]):
        if session_id not in selected_sessions:
            continue
        date = row["haystack_dates"][session_index]
        turns = row["haystack_sessions"][session_index]
        answer_turns = {i for i, turn in enumerate(turns) if turn.get("has_answer")}
        included_turns = longmem_included_turns(session_id in answer_sessions, answer_turns, len(turns))
        for turn_index in included_turns:
            turn = turns[turn_index]
            role = clean_text(turn.get("role", ""))
            text = clean_text(turn.get("content", ""))
            if not text:
                continue
            is_required = bool(turn.get("has_answer"))
            prefix = f"LongMemEval-S session {session_id} at {date}. {role}:"
            chunks = split_with_prefix(prefix, text, max_codepoints)
            for chunk_index, chunk in enumerate(chunks):
                source_doc_id = sanitize_id(f"{case_id}:session:{session_index}:turn:{turn_index}:chunk:{chunk_index}")
                rows.append(seed_row(source_doc_id, "LongMemEval-S chat turn", chunk, "longmemeval_s", "longmem_chat_turn", case_id, is_required, {
                    "dataset": "longmemeval_s",
                    "question_id": raw_id,
                    "question_type": row["question_type"],
                    "session_id": session_id,
                    "session_index": session_index,
                    "turn_index": turn_index,
                    "chunk_index": chunk_index,
                    "has_answer": is_required,
                    "question_date": row.get("question_date"),
                    "session_date": date,
                }))
                if is_required:
                    required.append(source_doc_id)
    if not required or len(required) > 10:
        return None
    answer = clean_text(row.get("answer", ""))
    return SeedCase(
        case_id=case_id,
        query=clean_text(row["question"]),
        task_type=f"public_longmemeval_s_{row['question_type']}",
        difficulty=row["question_type"],
        slices=["public_semantic", "longmemeval_s", row["question_type"]],
        expected_behavior="retrieve chat turns labeled as evidence for the LongMemEval-S answer",
        limit=10,
        reference_answer=answer,
        must_include=[answer] if answer else [],
        required_source_doc_ids=required,
        corpus_rows=rows,
        transform={"dataset": "longmemeval_s", "question_id": raw_id, "question_type": row["question_type"], "required_chunks": len(required), "row_count": len(rows), "distractor_sessions": distractor_sessions},
    )


def longmem_distractor_indices(row: dict[str, Any], limit: int) -> list[int]:
    answer_sessions = set(row.get("answer_session_ids") or [])
    candidates = [i for i, sid in enumerate(row["haystack_session_ids"]) if sid not in answer_sessions]
    if len(candidates) <= limit:
        return candidates
    if limit <= 0:
        return []
    stride = max(1, len(candidates) // limit)
    out = []
    for index in range(0, len(candidates), stride):
        out.append(candidates[index])
        if len(out) >= limit:
            break
    return out


def longmem_included_turns(answer_session: bool, answer_turns: set[int], turn_count: int) -> list[int]:
    if not answer_session:
        return list(range(min(2, turn_count)))
    included = set(answer_turns)
    for turn in answer_turns:
        for neighbor in (turn - 1, turn + 1):
            if 0 <= neighbor < turn_count:
                included.add(neighbor)
    if not included:
        included.update(range(min(2, turn_count)))
    return sorted(included)


def seed_row(
    source_doc_id: str,
    title: str,
    content: str,
    dataset: str,
    source_type: str,
    case_id: str,
    required: bool,
    metadata: dict[str, Any],
) -> SeedRow:
    labels = ["eval", "public_semantic", dataset]
    merged_metadata = dict(metadata)
    merged_metadata.update({"case_id": case_id, "source_doc_id": source_doc_id})
    return SeedRow(
        source_doc_id=source_doc_id,
        title=clean_text(title),
        content=content,
        source_dataset=dataset,
        source_type=source_type,
        authority="public_benchmark",
        source_quality=0.8,
        labels=labels,
        metadata=merged_metadata,
    )


def split_with_prefix(prefix: str, text: str, max_codepoints: int) -> list[str]:
    prefix = clean_text(prefix)
    text = clean_text(text)
    if not text:
        return []
    available = max_codepoints - len(prefix) - 1
    if available < 200:
        prefix = prefix[: min(len(prefix), 180)]
        available = max_codepoints - len(prefix) - 1
    parts = split_text(text, available)
    out = []
    for part in parts:
        value = f"{prefix}\n{part}".strip() if prefix else part
        if len(value) > max_codepoints:
            raise ValueError(f"split produced {len(value)} code points; max {max_codepoints}")
        out.append(value)
    return out


def split_text(text: str, max_codepoints: int) -> list[str]:
    if len(text) <= max_codepoints:
        return [text]
    units = re.split(r"(?<=[.!?])\s+", text)
    chunks: list[str] = []
    current = ""
    for unit in units:
        if not unit:
            continue
        if len(unit) > max_codepoints:
            if current:
                chunks.append(current)
                current = ""
            chunks.extend(split_long_unit(unit, max_codepoints))
            continue
        proposed = f"{current} {unit}".strip()
        if len(proposed) <= max_codepoints:
            current = proposed
        else:
            if current:
                chunks.append(current)
            current = unit
    if current:
        chunks.append(current)
    return chunks


def split_long_unit(text: str, max_codepoints: int) -> list[str]:
    words = text.split()
    chunks: list[str] = []
    current = ""
    for word in words:
        if len(word) > max_codepoints:
            if current:
                chunks.append(current)
                current = ""
            chunks.extend(word[i : i + max_codepoints] for i in range(0, len(word), max_codepoints))
            continue
        proposed = f"{current} {word}".strip()
        if len(proposed) <= max_codepoints:
            current = proposed
        else:
            if current:
                chunks.append(current)
            current = word
    if current:
        chunks.append(current)
    return chunks


def write_seed(seed_dir: Path, suite_path: Path, seed_id: str, cases: list[SeedCase], args: argparse.Namespace) -> None:
    counts = {
        "corpus": sum(len(case.corpus_rows) for case in cases),
        "cases": len(cases),
        "qrels": len(cases),
        "answers": len(cases),
        "transforms": len(cases),
    }
    with (
        (seed_dir / "corpus.jsonl").open("w", encoding="utf-8") as corpus_out,
        (seed_dir / "cases.jsonl").open("w", encoding="utf-8") as cases_out,
        (seed_dir / "qrels.jsonl").open("w", encoding="utf-8") as qrels_out,
        (seed_dir / "answers.jsonl").open("w", encoding="utf-8") as answers_out,
        (seed_dir / "transforms.jsonl").open("w", encoding="utf-8") as transforms_out,
        suite_path.open("w", encoding="utf-8") as suite_out,
    ):
        for case in cases:
            for row in case.corpus_rows:
                write_jsonl_row(corpus_out, row.__dict__)
            write_jsonl_row(cases_out, {
                "case_id": case.case_id,
                "query": case.query,
                "task_type": case.task_type,
                "difficulty": case.difficulty,
                "slices": case.slices,
                "expected_behavior": case.expected_behavior,
                "limit": case.limit,
            })
            write_jsonl_row(qrels_out, {
                "case_id": case.case_id,
                "required_refs": [
                    {"type": "source_doc", "source_doc_id": source_doc_id, "grade": 1, "reason": "public benchmark evidence"}
                    for source_doc_id in case.required_source_doc_ids
                ],
                "required_evidence_refs": [
                    {"type": "source_doc", "source_doc_id": source_doc_id, "grade": 1, "reason": "public benchmark evidence"}
                    for source_doc_id in case.required_source_doc_ids
                ],
            })
            write_jsonl_row(answers_out, {
                "case_id": case.case_id,
                "reference_answer": case.reference_answer,
                "must_include": case.must_include,
                "must_not_include": [],
                "expected_behavior": case.expected_behavior,
                "groundedness_policy": "public_semantic_qrels",
            })
            transform = dict(case.transform)
            transform.update({"case_id": case.case_id, "required_source_doc_ids": case.required_source_doc_ids})
            write_jsonl_row(transforms_out, transform)
            write_jsonl_row(suite_out, {"case_id": case.case_id, "weight": 1, "slices": case.slices})
    sources = [
        {"name": "MuSiQue Answerable dev v1.0", "url": "https://github.com/StonyBrookNLP/musique", "license": "CC BY 4.0"},
		{"name": "QASPER train/dev v0.3", "url": QASPER_TRAIN_DEV_URL, "license": "CC BY 4.0"},
        {"name": "LongMemEval-S cleaned", "url": LONGMEMEVAL_S_URL, "license": "See upstream dataset card/repository"},
    ]
    write_licenses(seed_dir / "licenses.md", sources)
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "seed_id": seed_id,
        "description": "Public semantic 3-axis seed: MuSiQue multi-hop, QASPER paper QA, LongMemEval-S memory QA.",
        "generated_at": deterministic_generated_at(),
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
    write_json(seed_dir / "public_eval_manifest.json", {
        "seed_id": seed_id,
        "generated_at": manifest["generated_at"],
        "max_evidence_codepoints": args.max_evidence_codepoints,
        "preflight": args.preflight,
        "quotas": quota_summary(cases),
        "distractor_policy": {
            "musique_distractors_per_case": args.musique_distractors,
            "qasper_distractors_per_case": args.qasper_distractors,
            "longmem_distractor_sessions_per_case": args.longmem_distractor_sessions,
        },
        "counts": counts,
        "seed_hash": seed_hash(seed_dir),
    })


def validate_cases(cases: list[SeedCase], expected_cases: int) -> None:
    if len(cases) != expected_cases:
        raise SystemExit(f"case count = {len(cases)}; want {expected_cases}")
    case_ids = set()
    source_doc_ids = set()
    for case in cases:
        if case.case_id in case_ids:
            raise SystemExit(f"duplicate case_id {case.case_id}")
        case_ids.add(case.case_id)
        if not case.query or not case.reference_answer:
            raise SystemExit(f"case {case.case_id} missing query or reference answer")
        if not case.required_source_doc_ids:
            raise SystemExit(f"case {case.case_id} has no qrels")
        row_ids = {row.source_doc_id for row in case.corpus_rows}
        for source_doc_id in case.required_source_doc_ids:
            if source_doc_id not in row_ids:
                raise SystemExit(f"case {case.case_id} qrel {source_doc_id} missing from corpus rows")
        for row in case.corpus_rows:
            if row.source_doc_id in source_doc_ids:
                raise SystemExit(f"duplicate source_doc_id {row.source_doc_id}")
            source_doc_ids.add(row.source_doc_id)
            if not row.content:
                raise SystemExit(f"{row.source_doc_id} has empty content")
            if len(row.content) > MAX_EVIDENCE_CODEPOINTS:
                raise SystemExit(f"{row.source_doc_id} has {len(row.content)} code points")
            for forbidden in ("required", "distractor"):
                if forbidden in row.labels:
                    raise SystemExit(f"{row.source_doc_id} leaks relevance label {forbidden}")
                if forbidden in row.metadata:
                    raise SystemExit(f"{row.source_doc_id} leaks relevance metadata {forbidden}")
    summary = quota_summary(cases)
    if expected_cases == 1000:
        expected = {
            "musique:2_hop": 150,
            "musique:3_hop": 150,
            "musique:4_hop": 150,
            "qasper:extractive:single": 130,
            "qasper:extractive:multi": 130,
            "qasper:freeform:single": 25,
            "qasper:freeform:multi": 25,
            "qasper:yesno:single": 15,
            "qasper:yesno:multi": 15,
            "qasper:mixed:single": 45,
            "qasper:mixed:multi": 45,
            "longmemeval_s:single-session-user": 20,
            "longmemeval_s:single-session-assistant": 20,
            "longmemeval_s:single-session-preference": 20,
            "longmemeval_s:temporal-reasoning": 20,
            "longmemeval_s:knowledge-update": 20,
            "longmemeval_s:multi-session": 20,
        }
        for key, want in expected.items():
            got = summary.get(key, 0)
            if got != want:
                raise SystemExit(f"quota {key} = {got}; want {want}")


def quota_summary(cases: list[SeedCase]) -> dict[str, int]:
    out = Counter()
    for case in cases:
        if "musique" in case.slices:
            out[f"musique:{case.difficulty}"] += 1
        elif "qasper" in case.slices:
            out[f"qasper:{case.slices[2]}:{case.slices[3]}"] += 1
        elif "longmemeval_s" in case.slices:
            out[f"longmemeval_s:{case.difficulty}"] += 1
    return dict(sorted(out.items()))


def require_quotas(label: str, counts: Counter, quotas: dict[Any, int]) -> None:
    missing = {str(key): value - counts[key] for key, value in quotas.items() if counts[key] < value}
    if missing:
        raise SystemExit(f"{label} quotas not met: {missing}")


def clean_text(value: Any) -> str:
    return re.sub(r"\s+", " ", str(value or "")).strip()


def unique_texts(values: Iterable[Any]) -> list[str]:
    out = []
    seen = set()
    for value in values:
        text = clean_text(value)
        if text and text not in seen:
            seen.add(text)
            out.append(text)
    return out


def sanitize_id(value: str) -> str:
    value = re.sub(r"[^A-Za-z0-9._:-]+", "_", value.strip())
    value = re.sub(r"_+", "_", value).strip("_")
    if len(value) <= 220:
        return value
    digest = hashlib.sha1(value.encode("utf-8")).hexdigest()[:16]
    return f"{value[:200]}:{digest}"


def deterministic_generated_at() -> str:
    raw = os.environ.get("SOURCE_DATE_EPOCH", "").strip()
    if not raw:
        return DETERMINISTIC_GENERATED_AT
    try:
        epoch = int(raw)
    except ValueError as exc:
        raise SystemExit("SOURCE_DATE_EPOCH must be an integer Unix timestamp") from exc
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(epoch))


def seed_hash(seed_dir: Path) -> str:
    digest = hashlib.sha256()
    for name in ["seed_manifest.json", "corpus.jsonl", "cases.jsonl", "qrels.jsonl", "answers.jsonl", "transforms.jsonl", "licenses.md"]:
        path = seed_dir / name
        digest.update(f"file:{name}\n".encode("utf-8"))
        digest.update(path.read_bytes())
        digest.update(b"\n")
    return "sha256:" + digest.hexdigest()


def write_json(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as handle:
        json.dump(value, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")


def write_jsonl_row(handle: TextIO, row: dict[str, Any]) -> None:
    json.dump(row, handle, ensure_ascii=False, sort_keys=True)
    handle.write("\n")


def write_licenses(path: Path, sources: list[dict[str, str]]) -> None:
    lines = [
        "# Public Semantic Seed Sources",
        "",
        "This seed pack is generated locally from public benchmark datasets.",
        "Do not commit generated seed packs unless upstream licenses have been reviewed for that purpose.",
        "",
    ]
    for source in sources:
        lines.extend([
            f"## {source['name']}",
            "",
            f"- URL: {source['url']}",
            f"- License: {source.get('license', 'See upstream metadata')}",
            "",
        ])
    path.write_text("\n".join(lines), encoding="utf-8")


def warn(message: str) -> None:
    print(message, flush=True)


if __name__ == "__main__":
    raise SystemExit(main())
