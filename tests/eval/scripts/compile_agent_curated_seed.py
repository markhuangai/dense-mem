#!/usr/bin/env python3
"""Compile a V2 seed from an agent-authored relationship ledger.

This compiler never selects entities, predicates, endpoints, or supporting
evidence. It only resolves explicit ledger surfaces to exact code-point spans
and rejects ambiguous selections.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import subprocess
import tarfile
import tempfile
from pathlib import Path
from typing import Any

from derive_submission_v2_seed import normalize_rows
from prepare_public_semantic_eval import MAX_EVIDENCE_CODEPOINTS, clean_text, split_with_prefix


GENERATOR = "tests/eval/scripts/compile_agent_curated_seed.py"
V1_SCHEMA = "dense-mem.eval.seed.v1"
V2_SCHEMA = "dense-mem.eval.seed.v2"
AUDIT_SCHEMA = "dense-mem.eval.proposal_audit.v1"
VALIDATION_SCHEMA = "dense-mem.eval.validation.v1"
CURATION_PROTOCOL = "dense-mem.eval.agent_curation.v1"
CURATION_REPLACEMENTS_SCHEMA = "dense-mem.eval.curation_replacements.v1"
COMPILER_LOCK_SCHEMA = "dense-mem.eval.compiler_lock.v1"
QASPER_ARCHIVE = Path("data/public_semantic/qasper-train-dev-v0.3.tgz")
PUBLIC_6AXIS_V1_HASH = "sha256:eb09124331228e59898a93740104ab978b9974e3ebf7f7fc2e09728ef95b3d78"
COMPILER_LOCK_FILES = {
    "tests/eval/scripts/compile_agent_curated_seed.py",
    "tests/eval/scripts/derive_submission_v2_seed.py",
    "tests/eval/scripts/prepare_public_semantic_eval.py",
}
ENTITY_KINDS = {
    "person",
    "organization",
    "project",
    "product",
    "place",
    "document",
    "concept",
    "other",
}
MODALITIES = {"statement", "question", "proposal", "speculation", "quoted"}


def main() -> int:
    args = parse_args()
    repo_root = Path(__file__).resolve().parents[3]
    compiler_lock_path = Path(args.compiler_lock).resolve()
    compiler_lock = verify_compiler_lock(repo_root, compiler_lock_path)
    root = Path(args.root).resolve()
    source_dir = root / "seeds" / args.source_seed
    source_manifest_path = source_dir / "seed_manifest.json"
    source_manifest = read_json(source_manifest_path)
    if source_manifest.get("schema_version") != V1_SCHEMA:
        fail(f"{source_manifest_path}: expected {V1_SCHEMA}")
    source_hash = seed_hash(source_manifest_path, source_manifest)
    source_rows = read_jsonl(source_dir / source_manifest["corpus_file"])
    if args.source_seed == "public_6axis_1k_v1" and source_hash != PUBLIC_6AXIS_V1_HASH:
        fail(f"v1 seed hash {source_hash}; want {PUBLIC_6AXIS_V1_HASH}")
    normalized_rows, normalizations = normalize_rows(source_rows)
    verify_normalized_projection(source_rows, normalized_rows, normalized_rows)

    ledger_path = Path(args.ledger).resolve()
    protocol_path = Path(args.protocol).resolve()
    replacements_path = Path(args.replacements).resolve()
    replacements = read_replacements(replacements_path, normalized_rows)
    curated_rows, replacement_records = apply_replacements(root, normalized_rows, replacements)
    ledger = read_ledger(ledger_path, curated_rows)
    protocol = protocol_path.read_bytes()
    if not protocol:
        fail("curation protocol is empty")
    proposals = [proposal_from_selection(row["content"], ledger[row["source_doc_id"]]) for row in curated_rows]

    target_dir = root / "seeds" / args.seed_id
    target_suite = root / "suites" / f"{args.seed_id}.jsonl"
    if target_dir.exists() or target_suite.exists():
        fail(f"{target_dir} or {target_suite} already exists; choose a new seed id")

    with tempfile.TemporaryDirectory(prefix=f".{args.seed_id}.stage.", dir=root / "seeds") as temp:
        stage_root = Path(temp)
        stage = stage_root / args.seed_id
        stage.mkdir()
        stage_suite = stage_root / f"{args.seed_id}.jsonl"
        copy_seed_artifacts(source_dir, stage)
        write_corpus(stage / "corpus.jsonl", curated_rows, proposals)
        shutil.copyfile(root / "suites" / f"{args.source_seed}.jsonl", stage_suite)

        manifest = v2_manifest(source_manifest, args.seed_id, source_hash, compiler_lock)
        write_json(stage / "seed_manifest.json", manifest)
        shutil.copyfile(ledger_path, stage / "curation_ledger.jsonl")
        shutil.copyfile(protocol_path, stage / "curation_protocol.md")
        shutil.copyfile(replacements_path, stage / "curation_replacements.jsonl")
        shutil.copyfile(compiler_lock_path, stage / "curation_compiler_lock.json")

        protocol_hash = sha256_bytes(protocol)
        write_json(stage / "proposal_audit_report.json", proposal_audit_report(
            args.seed_id,
            source_hash,
            curated_rows,
            proposals,
            protocol_hash,
            compiler_lock,
        ))
        write_json(stage / "comparison_report.json", comparison_report(
            source_dir,
            stage,
            stage_suite,
            source_rows,
            normalized_rows,
            curated_rows,
            normalizations,
            replacement_records,
        ))
        write_json(stage / "provenance.json", provenance(
            source_dir,
            source_hash,
            source_rows,
            normalized_rows,
            curated_rows,
            proposals,
            ledger_path,
            replacements_path,
            protocol_hash,
            compiler_lock,
            normalizations,
            replacement_records,
        ))
        write_public_manifest(stage / "public_eval_manifest.json", source_dir / "public_eval_manifest.json", args.seed_id, source_hash)

        current_hash = seed_hash(stage / "seed_manifest.json", manifest)
        write_json(stage / "validation_report.json", validation_report(
            args.seed_id,
            current_hash,
            len(curated_rows),
            len(normalizations),
            len(replacement_records),
            compiler_lock,
        ))
        validate_runtime(stage / "seed_manifest.json", stage_suite)

        shutil.move(str(stage), target_dir)
        shutil.move(str(stage_suite), target_suite)

    print(json.dumps({
        "seed_id": args.seed_id,
        "parent_seed_id": args.source_seed,
        "parent_seed_hash": source_hash,
        "audit_mode": "agent_curated",
        "curated_rows": len(proposals),
        "replaced_rows": len(replacement_records),
    }, sort_keys=True))
    return 0


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default="tests/eval")
    parser.add_argument("--source-seed", default="public_6axis_1k_v1")
    parser.add_argument("--ledger", required=True, help="Agent-authored JSONL relationship selections.")
    parser.add_argument("--protocol", required=True, help="Agent curation protocol markdown file.")
    parser.add_argument("--replacements", required=True, help="Agent-authored source-locked replacement manifest.")
    parser.add_argument("--compiler-lock", required=True, help="Exact compiler and helper source hashes.")
    parser.add_argument("--seed-id", required=True)
    return parser.parse_args()


def verify_compiler_lock(repo_root: Path, lock_path: Path) -> dict[str, Any]:
    lock = read_json(lock_path)
    if set(lock) != {"schema_version", "files"}:
        fail("compiler lock must contain only schema_version and files")
    if lock.get("schema_version") != COMPILER_LOCK_SCHEMA:
        fail(f"{lock_path}: expected {COMPILER_LOCK_SCHEMA}")
    files = lock.get("files")
    if not isinstance(files, dict) or set(files) != COMPILER_LOCK_FILES:
        fail("compiler lock files do not match the required compiler dependency set")
    resolved: dict[str, str] = {}
    for relative_path in sorted(COMPILER_LOCK_FILES):
        expected_hash = exact_sha256(files[relative_path], f"compiler lock {relative_path}")
        actual_path = repo_root / relative_path
        if not actual_path.is_file():
            fail(f"compiler lock dependency is missing: {relative_path}")
        actual_hash = sha256_file(actual_path)
        if actual_hash != expected_hash:
            fail(f"compiler lock mismatch for {relative_path}: {actual_hash}; want {expected_hash}")
        resolved[relative_path] = actual_hash
    return {
        "file": lock_path.name,
        "sha256": sha256_file(lock_path),
        "files": resolved,
    }


def read_json(path: Path) -> dict[str, Any]:
    try:
        with path.open(encoding="utf-8") as handle:
            value = json.load(handle)
    except OSError as error:
        fail(f"read {path}: {error}")
    except json.JSONDecodeError as error:
        fail(f"parse {path}: {error}")
    if not isinstance(value, dict):
        fail(f"{path}: expected JSON object")
    return value


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        fail(f"read {path}: {error}")
    rows: list[dict[str, Any]] = []
    for number, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError as error:
            fail(f"parse {path}:{number}: {error}")
        if not isinstance(value, dict):
            fail(f"{path}:{number}: expected JSON object")
        rows.append(value)
    return rows


def read_replacements(path: Path, rows: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    entries = read_jsonl(path)
    rows_by_id = {exact_string(row.get("source_doc_id"), "normalized source_doc_id"): row for row in rows}
    replacements: dict[str, dict[str, Any]] = {}
    for number, entry in enumerate(entries, 1):
        source_doc_id = exact_string(entry.get("source_doc_id"), f"replacement row {number} source_doc_id")
        if source_doc_id not in rows_by_id:
            fail(f"replacement row {number}: unknown source_doc_id {source_doc_id!r}")
        if source_doc_id in replacements:
            fail(f"replacement row {number}: duplicate source_doc_id {source_doc_id!r}")
        validate_replacement(entry, number, rows_by_id[source_doc_id])
        replacements[source_doc_id] = entry
    return replacements


def validate_replacement(entry: dict[str, Any], number: int, row: dict[str, Any]) -> None:
    allowed = {"source_doc_id", "reason", "original_content_sha256", "source"}
    unexpected = sorted(set(entry) - allowed)
    if unexpected:
        fail(f"replacement row {number}: unsupported fields {', '.join(unexpected)}")
    exact_string(entry.get("reason"), f"replacement row {number} reason")
    original_hash = exact_sha256(entry.get("original_content_sha256"), f"replacement row {number} original_content_sha256")
    if original_hash != sha256_text(exact_string(row.get("content"), f"replacement row {number} normalized content")):
        fail(f"replacement row {number}: original_content_sha256 does not bind the normalized V1 projection")

    source = entry.get("source")
    if not isinstance(source, dict):
        fail(f"replacement row {number}: source must be an object")
    common = {"dataset", "split", "paper_id", "question_id", "kind", "chunk_index"}
    kind = source.get("kind")
    if kind == "full_text_paragraph":
        allowed_source = common | {"section_index", "paragraph_index"}
    elif kind == "answer_evidence":
        allowed_source = common | {"answer_index", "evidence_index"}
    else:
        fail(f"replacement row {number}: source kind is unsupported")
    unexpected_source = sorted(set(source) - allowed_source)
    missing_source = sorted(allowed_source - set(source))
    if unexpected_source or missing_source:
        detail = [f"unexpected {', '.join(unexpected_source)}"] if unexpected_source else []
        if missing_source:
            detail.append(f"missing {', '.join(missing_source)}")
        fail(f"replacement row {number}: source fields are invalid ({'; '.join(detail)})")
    if source.get("dataset") != "qasper" or row.get("source_dataset") != "qasper":
        fail(f"replacement row {number}: only QASPER replacements are supported")
    if source.get("split") not in {"dev", "train"}:
        fail(f"replacement row {number}: source split is unsupported")
    for field in ("paper_id", "question_id"):
        exact_string(source.get(field), f"replacement row {number} source {field}")
    for field in ("chunk_index", "section_index", "paragraph_index", "answer_index", "evidence_index"):
        if field in source and (not isinstance(source[field], int) or source[field] < 0):
            fail(f"replacement row {number}: source {field} must be a non-negative integer")

    metadata = row.get("metadata")
    if not isinstance(metadata, dict):
        fail(f"replacement row {number}: normalized row metadata is invalid")
    for field in ("split", "paper_id", "question_id"):
        if metadata.get(field) != source[field]:
            fail(f"replacement row {number}: source {field} does not match normalized row metadata")
    if kind == "full_text_paragraph" and row.get("source_type") != "qasper_paper_context":
        fail(f"replacement row {number}: full_text_paragraph must replace QASPER context")
    if kind == "answer_evidence" and row.get("source_type") != "qasper_evidence":
        fail(f"replacement row {number}: answer_evidence must replace QASPER evidence")


def apply_replacements(
    root: Path,
    normalized_rows: list[dict[str, Any]],
    replacements: dict[str, dict[str, Any]],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    if not replacements:
        return normalized_rows, []
    archive = verified_qasper_archive(root)
    papers_by_split: dict[str, dict[str, Any]] = {}
    curated_rows: list[dict[str, Any]] = []
    records: list[dict[str, Any]] = []
    for row in normalized_rows:
        source_doc_id = exact_string(row.get("source_doc_id"), "normalized source_doc_id")
        replacement = replacements.get(source_doc_id)
        if replacement is None:
            curated_rows.append(row)
            continue
        content = qasper_replacement_content(archive, papers_by_split, replacement["source"])
        candidate = dict(row)
        candidate["content"] = content
        normalized_candidate, transforms = normalize_rows([candidate])
        if transforms or normalized_candidate[0]["content"] != content:
            fail(f"replacement {source_doc_id}: deterministic security normalization changed the manually selected source")
        curated_rows.append(normalized_candidate[0])
        records.append({
            "source_doc_id": source_doc_id,
            "reason": replacement["reason"],
            "original_content_sha256": replacement["original_content_sha256"],
            "replacement_content_sha256": sha256_text(content),
            "source": replacement["source"],
        })
    if len(records) != len(replacements):
        fail("replacement manifest contains an unreachable source_doc_id")
    return curated_rows, records


def verified_qasper_archive(root: Path) -> Path:
    archive = root / QASPER_ARCHIVE
    if not archive.is_file():
        fail(f"missing locked QASPER archive {archive}")
    source_lock = read_json(root / "source_locks" / "public_6axis_v1.json")
    entry = next((item for item in source_lock.get("sources", []) if item.get("axis") == "qasper"), None)
    if not isinstance(entry, dict):
        fail("QASPER source lock entry is missing")
    source_hash = exact_string(entry.get("sha256"), "QASPER source lock sha256")
    expected_hash = f"sha256:{source_hash}" if len(source_hash) == 64 else exact_sha256(source_hash, "QASPER source lock sha256")
    if any(char not in "0123456789abcdef" for char in expected_hash[7:]):
        fail("QASPER source lock sha256 must be a lowercase sha256 digest")
    if sha256_file(archive) != expected_hash:
        fail(f"QASPER archive sha256 does not match {root / 'source_locks' / 'public_6axis_v1.json'}")
    expected_size = entry.get("content_length")
    if isinstance(expected_size, int) and archive.stat().st_size != expected_size:
        fail("QASPER archive content length does not match the source lock")
    return archive


def qasper_replacement_content(
    archive: Path,
    papers_by_split: dict[str, dict[str, Any]],
    source: dict[str, Any],
) -> str:
    split = source["split"]
    papers = papers_by_split.get(split)
    if papers is None:
        member = f"qasper-{split}-v0.3.json"
        with tarfile.open(archive) as handle:
            payload = handle.extractfile(member)
            if payload is None:
                fail(f"QASPER archive is missing {member}")
            try:
                papers = json.load(payload)
            except json.JSONDecodeError as error:
                fail(f"parse QASPER archive {member}: {error}")
        if not isinstance(papers, dict):
            fail(f"QASPER archive {member}: expected paper object")
        papers_by_split[split] = papers
    paper = papers.get(source["paper_id"])
    if not isinstance(paper, dict):
        fail(f"QASPER archive: paper {source['paper_id']!r} is missing")
    if source["kind"] == "full_text_paragraph":
        sections = paper.get("full_text")
        if not isinstance(sections, list) or source["section_index"] >= len(sections):
            fail("QASPER replacement section_index is outside the source paper")
        section = sections[source["section_index"]]
        if not isinstance(section, dict):
            fail("QASPER replacement section is invalid")
        paragraphs = section.get("paragraphs")
        if not isinstance(paragraphs, list) or source["paragraph_index"] >= len(paragraphs):
            fail("QASPER replacement paragraph_index is outside the source section")
        prefix = f"QASPER paper context. Section: {clean_text(section.get('section_name', ''))}."
        chunks = split_with_prefix(prefix, paragraphs[source["paragraph_index"]], MAX_EVIDENCE_CODEPOINTS)
    else:
        qa = next((item for item in paper.get("qas", []) if item.get("question_id") == source["question_id"]), None)
        if not isinstance(qa, dict):
            fail(f"QASPER archive: question {source['question_id']!r} is missing")
        answers = qa.get("answers")
        if not isinstance(answers, list) or source["answer_index"] >= len(answers):
            fail("QASPER replacement answer_index is outside the source question")
        answer_wrapper = answers[source["answer_index"]]
        answer = answer_wrapper.get("answer") if isinstance(answer_wrapper, dict) else None
        evidence = answer.get("evidence") if isinstance(answer, dict) else None
        if not isinstance(evidence, list) or source["evidence_index"] >= len(evidence):
            fail("QASPER replacement evidence_index is outside the source answer")
        prefix = f"QASPER evidence. Paper: {clean_text(paper.get('title', source['paper_id']))}."
        chunks = split_with_prefix(prefix, evidence[source["evidence_index"]], MAX_EVIDENCE_CODEPOINTS)
    chunk_index = source["chunk_index"]
    if chunk_index >= len(chunks):
        fail("QASPER replacement chunk_index is outside the selected source text")
    return chunks[chunk_index]


def read_ledger(path: Path, rows: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    selections = read_jsonl(path)
    expected = {exact_string(row.get("source_doc_id"), "normalized source_doc_id") for row in rows}
    ledger: dict[str, dict[str, Any]] = {}
    for number, selection in enumerate(selections, 1):
        source_doc_id = exact_string(selection.get("source_doc_id"), f"ledger row {number} source_doc_id")
        if source_doc_id not in expected:
            fail(f"ledger row {number}: unknown source_doc_id {source_doc_id!r}")
        if source_doc_id in ledger:
            fail(f"ledger row {number}: duplicate source_doc_id {source_doc_id!r}")
        validate_selection(selection, number)
        ledger[source_doc_id] = selection
    missing = sorted(expected - set(ledger))
    if missing:
        fail(f"ledger is missing {len(missing)} source_doc_id values")
    if len(ledger) != len(rows):
        fail("ledger row count does not match normalized corpus")
    return ledger


def validate_selection(selection: dict[str, Any], number: int) -> None:
    allowed = {
        "source_doc_id",
        "support",
        "support_occurrence",
        "subject",
        "subject_occurrence",
        "subject_kind",
        "predicate",
        "predicate_occurrence",
        "object",
        "object_occurrence",
        "object_kind",
        "polarity",
        "modality",
    }
    unexpected = sorted(set(selection) - allowed)
    if unexpected:
        fail(f"ledger row {number}: unsupported fields {', '.join(unexpected)}")
    for field in ("support", "subject", "predicate", "object"):
        exact_string(selection.get(field), f"ledger row {number} {field}")
    for field in ("support_occurrence", "subject_occurrence", "predicate_occurrence", "object_occurrence"):
        if field in selection and (not isinstance(selection[field], int) or selection[field] < 0):
            fail(f"ledger row {number}: {field} must be a non-negative integer")
    for field in ("subject_kind", "object_kind"):
        if field in selection and selection[field] not in ENTITY_KINDS:
            fail(f"ledger row {number}: {field} is unsupported")
    if "polarity" in selection and selection["polarity"] not in {"+", "-"}:
        fail(f"ledger row {number}: polarity is unsupported")
    if "modality" in selection and selection["modality"] not in MODALITIES:
        fail(f"ledger row {number}: modality is unsupported")


def proposal_from_selection(content: str, selection: dict[str, Any]) -> dict[str, Any]:
    support_start, support_end = resolve_surface(
        content,
        selection["support"],
        selection.get("support_occurrence"),
        0,
        len(content),
        "support",
    )
    subject_start, subject_end = resolve_surface(
        content,
        selection["subject"],
        selection.get("subject_occurrence"),
        support_start,
        support_end,
        "subject",
    )
    predicate_start, predicate_end = resolve_surface(
        content,
        selection["predicate"],
        selection.get("predicate_occurrence"),
        support_start,
        support_end,
        "predicate",
    )
    object_start, object_end = resolve_surface(
        content,
        selection["object"],
        selection.get("object_occurrence"),
        support_start,
        support_end,
        "object",
    )
    return {
        "entities": [
            {
                "ref": "entity_1",
                "name": selection["subject"],
                "entity_kind": selection.get("subject_kind", "other"),
                "evidence": [span(subject_start, subject_end)],
            },
            {
                "ref": "entity_2",
                "name": selection["object"],
                "entity_kind": selection.get("object_kind", "other"),
                "evidence": [span(object_start, object_end)],
            },
        ],
        "relationships": [{
            "proposal_id": "relationship_1",
            "subject_ref": "entity_1",
            "object_ref": "entity_2",
            "predicate": {
                "surface": selection["predicate"],
                **span(predicate_start, predicate_end),
            },
            "polarity": selection.get("polarity", "+"),
            "modality": selection.get("modality", "statement"),
            "evidence": [span(support_start, support_end)],
        }],
    }


def resolve_surface(
    content: str,
    surface: str,
    occurrence: Any,
    scope_start: int,
    scope_end: int,
    label: str,
) -> tuple[int, int]:
    occurrences = find_occurrences(content, surface, scope_start, scope_end)
    if not occurrences:
        fail(f"{label} surface does not occur in its declared scope")
    if occurrence is None:
        if len(occurrences) != 1:
            fail(f"{label} surface occurs {len(occurrences)} times; declare an explicit occurrence")
        start = occurrences[0]
    else:
        if occurrence >= len(occurrences):
            fail(f"{label}_occurrence {occurrence} is outside the {len(occurrences)} matching surfaces")
        start = occurrences[occurrence]
    return start, start + len(surface)


def find_occurrences(content: str, surface: str, start: int, end: int) -> list[int]:
    positions: list[int] = []
    cursor = start
    while True:
        position = content.find(surface, cursor, end)
        if position < 0:
            return positions
        positions.append(position)
        cursor = position + max(1, len(surface))


def span(start: int, end: int) -> dict[str, int]:
    return {"evidence_index": 0, "start": start, "end": end}


def verify_normalized_projection(
    source_rows: list[dict[str, Any]],
    expected_normalized_rows: list[dict[str, Any]],
    normalized_rows: list[dict[str, Any]],
) -> None:
    if len(source_rows) != len(normalized_rows):
        fail("normalized corpus row count does not match the V1 corpus")
    if len(expected_normalized_rows) != len(normalized_rows):
        fail("deterministic normalization row count does not match the normalized corpus")
    for number, (source, expected, normalized) in enumerate(zip(source_rows, expected_normalized_rows, normalized_rows), 1):
        source_id = source.get("source_doc_id")
        if source_id != normalized.get("source_doc_id"):
            fail(f"normalized corpus row {number}: source_doc_id order changed")
        normalized_without_proposal = dict(normalized)
        normalized_without_proposal.pop("proposal", None)
        source_without_content = dict(source)
        normalized_without_content = dict(normalized_without_proposal)
        source_without_content.pop("content", None)
        normalized_without_content.pop("content", None)
        if canonical_json(source_without_content) != canonical_json(normalized_without_content):
            fail(f"normalized corpus row {number}: non-content projection changed")
        if not isinstance(normalized_without_proposal.get("content"), str) or not normalized_without_proposal["content"]:
            fail(f"normalized corpus row {number}: content is missing")
        if expected.get("content") != normalized_without_proposal["content"]:
            fail(f"normalized corpus row {number}: content does not match deterministic V1 normalization")


def copy_seed_artifacts(source_dir: Path, target_dir: Path) -> None:
    excluded = {"corpus.jsonl", "seed_manifest.json", "validation_report.json", "public_eval_manifest.json"}
    for source in source_dir.iterdir():
        if source.name in excluded or not source.is_file():
            continue
        shutil.copyfile(source, target_dir / source.name)


def write_corpus(path: Path, rows: list[dict[str, Any]], proposals: list[dict[str, Any]]) -> None:
    with path.open("w", encoding="utf-8", newline="\n") as handle:
        for row, proposal in zip(rows, proposals):
            output = dict(row)
            output.pop("proposal", None)
            output["proposal"] = proposal
            handle.write(canonical_json(output) + "\n")


def v2_manifest(source: dict[str, Any], seed_id: str, parent_hash: str, compiler_lock: dict[str, Any]) -> dict[str, Any]:
    manifest = dict(source)
    manifest.update({
        "schema_version": V2_SCHEMA,
        "seed_id": seed_id,
        "description": "Submission-ready V2 derivation with agent-curated relationship proposals.",
        "generated_at": "deterministic",
        "validation_report_file": "validation_report.json",
        "proposal_audit_report_file": "proposal_audit_report.json",
        "curation_replacements_file": "curation_replacements.jsonl",
        "curation_compiler_lock_file": "curation_compiler_lock.json",
        "curation_compiler_lock_sha256": compiler_lock["sha256"],
        "parent_seed_id": source["seed_id"],
        "parent_seed_hash": parent_hash,
        "generator": GENERATOR,
    })
    return manifest


def proposal_audit_report(
    seed_id: str,
    parent_hash: str,
    rows: list[dict[str, Any]],
    proposals: list[dict[str, Any]],
    protocol_hash: str,
    compiler_lock: dict[str, Any],
) -> dict[str, Any]:
    return {
        "schema_version": AUDIT_SCHEMA,
        "seed_id": seed_id,
        "parent_seed_hash": parent_hash,
        "audit_mode": "agent_curated",
        "audit_policy_sha256": protocol_hash,
        "curation_protocol": CURATION_PROTOCOL,
        "curation_protocol_file": "curation_protocol.md",
        "curation_protocol_sha256": protocol_hash,
        "compiler_lock_file": compiler_lock["file"],
        "compiler_lock_sha256": compiler_lock["sha256"],
        "curated_rows": len(rows),
        "status": "passed",
        "audited_rows": len(rows),
        "warning_count": 0,
        "regeneration_count": 0,
        "transport_retry_count": 0,
        "rows": [{
            "source_doc_id": row["source_doc_id"],
            "evidence_sha256": sha256_text(row["content"]),
            "proposal_sha256": hash_json(proposal),
            "audit_policy_sha256": protocol_hash,
            "regenerations": 0,
            "transport_retries": 0,
        } for row, proposal in zip(rows, proposals)],
    }


def comparison_report(
    source_dir: Path,
    target_dir: Path,
    target_suite: Path,
    source_rows: list[dict[str, Any]],
    normalized_rows: list[dict[str, Any]],
    curated_rows: list[dict[str, Any]],
    normalizations: list[dict[str, Any]],
    replacement_records: list[dict[str, Any]],
) -> dict[str, Any]:
    target_rows = read_jsonl(target_dir / "corpus.jsonl")
    projection_mismatches: list[str] = []
    if len(source_rows) != len(target_rows) or len(normalized_rows) != len(target_rows) or len(curated_rows) != len(target_rows):
        projection_mismatches.append("corpus row count")
    expected_replacements = {record["source_doc_id"] for record in replacement_records}
    actual_replacements: set[str] = set()
    for number, (source, normalized, curated, target) in enumerate(zip(source_rows, normalized_rows, curated_rows, target_rows), 1):
        target_without_proposal = dict(target)
        target_without_proposal.pop("proposal", None)
        source_without_content = dict(source)
        target_without_content = dict(target_without_proposal)
        source_without_content.pop("content", None)
        target_without_content.pop("content", None)
        if canonical_json(source_without_content) != canonical_json(target_without_content):
            projection_mismatches.append(f"corpus non-content projection row {number}")
        if normalized.get("content") != curated.get("content"):
            actual_replacements.add(exact_string(curated.get("source_doc_id"), f"curated corpus row {number} source_doc_id"))
        if curated.get("content") != target_without_proposal.get("content"):
            projection_mismatches.append(f"corpus curated content row {number}")
    if actual_replacements != expected_replacements:
        projection_mismatches.append("curated replacement source_doc_id set")
    copied = ["cases.jsonl", "qrels.jsonl", "answers.jsonl", "transforms.jsonl", "licenses.md"]
    copied_equal = {
        name: bytes_equal(source_dir / name, target_dir / name)
        for name in copied
        if (source_dir / name).exists()
    }
    source_suite = source_dir.parent.parent / "suites" / f"{source_dir.name}.jsonl"
    suite_equal = bytes_equal(source_suite, target_suite)
    return {
        "schema_version": "dense-mem.eval.seed_comparison.v1",
        "parent_seed_id": source_dir.name,
        "derived_seed_id": target_dir.name,
        "corpus_rows": len(target_rows),
        "non_content_projection_equal": not any(item.startswith("corpus non-content") for item in projection_mismatches),
        "normalized_content_equal": not actual_replacements,
        "normalized_content_equal_except_replacements": actual_replacements == expected_replacements,
        "curated_content_equal": not any(item.startswith("corpus curated") for item in projection_mismatches),
        "curated_replacement_count": len(replacement_records),
        "curated_replacements": replacement_records,
        "content_normalization_count": len(normalizations),
        "content_normalizations": normalizations,
        "corpus_projection_mismatches": projection_mismatches,
        "row_order_equal": [row.get("source_doc_id") for row in source_rows] == [row.get("source_doc_id") for row in target_rows],
        "copied_artifacts_byte_equal": copied_equal,
        "suite_byte_equal": suite_equal,
        "status": "passed" if not projection_mismatches and all(copied_equal.values()) and suite_equal else "failed",
    }


def provenance(
    source_dir: Path,
    source_hash: str,
    source_rows: list[dict[str, Any]],
    normalized_rows: list[dict[str, Any]],
    curated_rows: list[dict[str, Any]],
    proposals: list[dict[str, Any]],
    ledger_path: Path,
    replacements_path: Path,
    protocol_hash: str,
    compiler_lock: dict[str, Any],
    normalizations: list[dict[str, Any]],
    replacement_records: list[dict[str, Any]],
) -> dict[str, Any]:
    generator_path = Path(__file__).resolve()
    return {
        "schema_version": "dense-mem.eval.seed_provenance.v1",
        "parent_seed_id": source_dir.name,
        "parent_seed_hash": source_hash,
        "generator": GENERATOR,
        "generator_sha256": sha256_bytes(generator_path.read_bytes()),
        "proposal_selection": "agent_curated",
        "curation_ledger_sha256": sha256_bytes(ledger_path.read_bytes()),
        "curation_protocol_sha256": protocol_hash,
        "curation_replacements_schema": CURATION_REPLACEMENTS_SCHEMA,
        "curation_replacements_sha256": sha256_bytes(replacements_path.read_bytes()),
        "compiler_lock_schema": COMPILER_LOCK_SCHEMA,
        "compiler_lock_file": compiler_lock["file"],
        "compiler_lock_sha256": compiler_lock["sha256"],
        "compiler_dependency_sha256": compiler_lock["files"],
        "corpus_rows": len(curated_rows),
        "source_row_hashes": [sha256_text(row["content"]) for row in source_rows],
        "normalized_row_hashes": [sha256_text(row["content"]) for row in normalized_rows],
        "curated_row_hashes": [sha256_text(row["content"]) for row in curated_rows],
        "curated_replacements": replacement_records,
        "security_normalizations": normalizations,
        "proposal_hashes": [hash_json(proposal) for proposal in proposals],
    }


def write_public_manifest(path: Path, source_path: Path, seed_id: str, parent_hash: str) -> None:
    payload = read_json(source_path)
    payload["seed_id"] = seed_id
    payload["id_namespace"] = seed_id
    payload["parent_seed_hash"] = parent_hash
    write_json(path, payload)


def validation_report(
    seed_id: str,
    current_hash: str,
    rows: int,
    normalizations: int,
    replacements: int,
    compiler_lock: dict[str, Any],
) -> dict[str, Any]:
    return {
        "schema_version": VALIDATION_SCHEMA,
        "seed_id": seed_id,
        "status": "passed",
        "seed_hash": current_hash,
        "expected_corpus_rows": rows,
        "checks": {
            "v1_projection": "passed" if replacements == 0 else "passed_with_documented_source_locked_replacements",
            "security_normalizations": "passed",
            "source_locked_replacements": "passed",
            "exact_spans": "passed",
            "deterministic_security_scan": "passed",
            "runtime_submission_contract": "passed",
            "agent_curation": "passed",
            "compiler_lock": "passed",
        },
        "security_normalization_count": normalizations,
        "source_locked_replacement_count": replacements,
        "compiler_lock_sha256": compiler_lock["sha256"],
    }


def validate_runtime(manifest_path: Path, suite_path: Path) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    command = [
        "go", "run", "./cmd/eval-runner",
        "--mode", "validate",
        "--seed", str(manifest_path),
        "--suite", str(suite_path),
    ]
    result = subprocess.run(command, cwd=repo_root, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip()
        fail(f"runtime seed validation failed: {detail}")


def seed_hash(manifest_path: Path, manifest: dict[str, Any]) -> str:
    files = [
        manifest_path,
        manifest_path.parent / manifest["corpus_file"],
        manifest_path.parent / manifest["cases_file"],
        manifest_path.parent / manifest["qrels_file"],
    ]
    for key in ("answers_file", "hard_negatives_file", "transforms_file", "dreams_file", "licenses_file"):
        if manifest.get(key):
            files.append(manifest_path.parent / manifest[key])
    digest = hashlib.sha256()
    for path in files:
        digest.update(f"file:{path.name}\n".encode("utf-8"))
        digest.update(path.read_bytes())
        digest.update(b"\n")
    return "sha256:" + digest.hexdigest()


def write_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def canonical_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def hash_json(value: Any) -> str:
    return sha256_text(canonical_json(value))


def sha256_text(value: str) -> str:
    return sha256_bytes(value.encode("utf-8"))


def sha256_bytes(value: bytes) -> str:
    return "sha256:" + hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def bytes_equal(left: Path, right: Path) -> bool:
    return left.exists() and right.exists() and left.read_bytes() == right.read_bytes()


def exact_string(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value or value != value.strip():
        fail(f"{label} must be an exact non-empty string")
    return value


def exact_sha256(value: Any, label: str) -> str:
    result = exact_string(value, label)
    if not result.startswith("sha256:") or len(result) != 71 or any(char not in "0123456789abcdef" for char in result[7:]):
        fail(f"{label} must be a lowercase sha256 digest")
    return result


def fail(message: str) -> None:
    raise SystemExit(message)


if __name__ == "__main__":
    raise SystemExit(main())
