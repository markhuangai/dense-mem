#!/usr/bin/env python3
"""Generate the frozen repository-owned session ingestion cohort."""

import argparse
import hashlib
import json
from pathlib import Path


def event(event_id, text):
    return {"event_id": event_id, "text": text}


def fact(subject, obj, polarity="+", **extra):
    return {"subject": subject, "predicate": "uses", "object": obj,
            "polarity": polarity, **extra}


def cohort():
    cases = []
    for index in range(8):
        name = f"Ari{index}"
        language = ["Go", "Rust", "Python", "Java", "Ruby", "Swift", "Kotlin", "TypeScript"][index]
        cases.append({"id": f"short-{index}", "group": "short",
                      "calls": [[event("one", f"{name} uses {language}.")]],
                      "expected": [fact(name, language)]})
    for index in range(8):
        name = f"Morgan{index}"
        statement = f"{name} uses Go."
        prefix = " " * (512 - len(name) - 1)
        text = (prefix + statement).ljust(32000)
        events = [event("one", text)]
        expected = [fact(name, "Go")]
        if index == 7:
            events = []
            expected = []
            for item in range(4):
                actor = f"MorganBatch{item}"
                events.append(event(str(item), f"{actor} uses Rust.".ljust(32000)))
                expected.append(fact(actor, "Rust"))
        elif index in (2, 3):
            text = (" café 🧭 " * 2000 + statement).ljust(32000)
            events = [event("one", text)]
        elif index in (4, 5):
            text = ("Um. " * 6200 + statement).ljust(32000)
            events = [event("one", text)]
        if index == 6:
            text = ("Um. " * 2686 + statement).ljust(32000)
            events = [event("one", text)]
        cases.append({"id": f"long-{index}", "group": "long", "calls": [events],
                      "expected": expected, "exact_provenance": True,
                      "designated_boundary": index in (0, 1, 6)})
    for index in range(8):
        name = f"Casey{index}"
        first = event("one", f"{name} uses Go.")
        second = event("two", f"{name} also uses Rust.")
        if index == 0:
            second = event("two", "They also use Rust.")
        elif index == 1:
            second = event("two", "He also uses Rust.")
        if index < 4:
            calls = [[first], [second]]
            expected = [fact(name, "Go"), fact(name, "Rust")]
        elif index < 6:
            calls = [[first], [first, second]]
            expected = [fact(name, "Go"), fact(name, "Rust")]
        else:
            other = f"Taylor{index}"
            calls = [[first], [event("two", f"{other} uses Rust.")]]
            expected = [fact(name, "Go"), fact(other, "Rust")]
        cases.append({"id": f"incremental-{index}", "group": "incremental",
                      "calls": calls, "expected": expected})
    semantic = [
        ("Avery does not use Redis.", [fact("Avery", "Redis", "-")]),
        ("Sam uses Go and does not use Rust.", [fact("Sam", "Go"), fact("Sam", "Rust", "-")]),
        ("Lee uses Go from 2026-01-01T00:00:00Z until 2026-06-01T00:00:00Z.",
         [fact("Lee", "Go", valid_from="2026-01-01T00:00:00Z", valid_to="2026-06-01T00:00:00Z")]),
        ("Jordan the designer uses Go. Jordan the musician uses Rust.",
         [fact("Jordan", "Go"), fact("Jordan", "Rust"),
          fact("Jordan", "designer", predicate="has_role"), fact("Jordan", "musician", predicate="has_role")]),
        ("Thanks. Let's continue.", []),
        ("Hello! Okay, thanks.", []),
        ("Quinn uses PostgreSQL. Quinn uses Redis.", [fact("Quinn", "PostgreSQL"), fact("Quinn", "Redis")]),
        ("Zoë uses Python. Renée uses Rust.", [fact("Zoë", "Python"), fact("Renée", "Rust")]),
    ]
    for index, (text, expected) in enumerate(semantic):
        case = {"id": f"semantics-{index}", "group": "semantics",
                "calls": [[event("one", text)]], "expected": expected}
        if index == 3:
            case["distinct_subject_ids"] = True
        cases.append(case)
    controls = remember_controls()
    return {"cohort": "session_ingest_v1", "quality_cases": cases, "remember_controls": controls}


def remember_controls():
    source = "tests/uat/synchronous_write/cases/remember.mjs"
    subject = "Dense-Mem"
    evidence = [{"content": "Dense-Mem stores durable memory in PostgreSQL. [fixture:control]", "source_type": "manual"}]
    proposal = {"ref": "durable-store", "subject": {"name": subject, "entity_kind": "project"},
                "predicate": {"proposed_key": "stores_memory_in"},
                "object": {"value": {"type": "string", "value": "PostgreSQL"}},
                "polarity": "+", "evidence_indices": [0]}
    base = {"evidence": evidence, "relationships": [proposal]}
    expected = [{"subject": subject, "predicate": "stores_memory_in", "object": "PostgreSQL", "polarity": "+"}]
    controls = []
    for name in ("missing", "empty", "uncited", "malformed"):
        args = json.loads(json.dumps(base))
        if name == "missing":
            del args["relationships"]
        elif name == "empty":
            args["relationships"] = []
        elif name == "uncited":
            args["evidence"].append({"content": "A second evidence item is uncited.", "source_type": "manual"})
        else:
            args["relationships"][0]["evidence_indices"] = [True]
        controls.append({"id": "required-" + name, "source": source + "#runRequiredRelationshipCase:" + name,
                         "operation": "invalid", "arguments": args, "expected": [],
                         "error": {"code": "invalid_input", "reason_code": "validation_failed", "rpc_code": -32602}})
    controls.append({"id": "provider-none", "source": source + "#runProviderFaultCase:none",
                     "operation": "success", "arguments": base, "expected": expected})
    subject = "Dense-Mem Mixed Objects frozen"
    database = "PostgreSQL Mixed Objects frozen"
    values = [
        {"type": "string", "value": "stable", "display": "Stable contract"},
        {"type": "number", "value": 42, "display": "42 ms", "unit": "ms"},
        {"type": "boolean", "value": True, "display": "Yes"},
        {"type": "date", "value": "2026-09-13", "display": "13 September 2026"},
        {"type": "date_time", "value": "2026-09-13T10:45:28Z", "display": "13 September 2026 at 10:45:28 UTC"},
    ]
    mixed = {"evidence": [{"content": f"{subject} stores its durable memory in {database}. [fixture:mixed-objects-entity]", "source_type": "manual"}],
             "relationships": [{"ref": "entity-object", "subject": {"name": subject, "entity_kind": "project"},
                                "predicate": {"known_predicate_key": "primary_database"},
                                "object": {"entity": {"name": database, "entity_kind": "product"}}, "polarity": "+", "evidence_indices": [0]}]}
    mixed_expected = [{"subject": subject, "predicate": "primary_database", "object": database, "polarity": "+"}]
    for index, value in enumerate(values):
        scalar = str(value["value"]).lower() if isinstance(value["value"], bool) else str(value["value"])
        predicate = "mixed_objects_" + value["type"] + "_frozen"
        mixed["evidence"].append({"content": f"{subject} records {value['type']} value {scalar}, displayed as {value['display']}. [fixture:mixed-objects-value]", "source_type": "manual"})
        mixed["relationships"].append({"ref": "typed-" + value["type"], "subject": {"name": subject, "entity_kind": "project"},
                                       "predicate": {"proposed_key": predicate}, "object": {"value": value}, "polarity": "+", "evidence_indices": [index + 1]})
        mixed_expected.append({"subject": subject, "predicate": predicate, "object": scalar, "polarity": "+"})
    controls.append({"id": "mixed-objects", "source": source + "#runMixedObjectCase", "operation": "success", "arguments": mixed, "expected": mixed_expected})
    registry_source = "internal/tools/registry/contract_relationship_submission_test.go"
    registry_base = {"evidence": [{"content": "Dense-Mem uses PostgreSQL."}],
                     "relationships": [{"ref": "uses-postgresql", "subject": {"name": "Dense-Mem", "entity_kind": "project"},
                                        "predicate": {"proposed_key": "uses"}, "object": {"entity": {"name": "PostgreSQL", "entity_kind": "product"}}, "polarity": "+", "evidence_indices": [0]}]}
    for name in ("legacy-proposal", "subject-span", "predicate-surface", "supports"):
        args = json.loads(json.dumps(registry_base))
        if name == "legacy-proposal":
            args["proposal"] = {}
        elif name == "subject-span":
            args["relationships"][0]["subject"]["span"] = {"evidence_index": 0, "start": 0, "end": 9}
        elif name == "predicate-surface":
            args["relationships"][0]["predicate"]["surface"] = "uses"
        else:
            args["relationships"][0]["supports"] = [{"evidence_index": 0, "start": 0, "end": 26}]
        anchor = "TestRememberRequiresRelationships:legacy" if name == "legacy-proposal" else "TestRememberRejectsFormerPublicGroundingFields:" + name
        controls.append({"id": name, "source": registry_source + "#" + anchor, "operation": "invalid", "arguments": args,
                         "expected": [], "error": {"code": "invalid_input", "reason_code": "validation_failed", "rpc_code": -32602}})
    for operation, function in (("concurrent", "runConcurrentWinnerCase"), ("conflict", "runChangedHashConflictCase")):
        args = json.loads(json.dumps(base))
        args["evidence"][0]["content"] = "Dense-Mem stores durable memory in PostgreSQL. [fixture:" + ("concurrent" if operation == "concurrent" else "conflict-a") + "]"
        controls.append({"id": operation, "source": source + "#" + function,
                         "operation": operation, "arguments": args, "expected": expected})
    return controls


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    payload = json.dumps(cohort(), ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n"
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(payload)
    print(json.dumps({"cohort": "session_ingest_v1", "cases": 32, "controls": 12,
                      "sha256": hashlib.sha256(payload.encode()).hexdigest()}))


if __name__ == "__main__":
    main()
