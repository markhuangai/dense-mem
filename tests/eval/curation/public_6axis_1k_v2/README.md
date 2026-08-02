# Public six-axis V2 curation source

This directory is the canonical, reviewable source for the reusable
public_6axis_1k_v2 evaluation seed. Generated seed packs and evaluation
outputs remain ignored.

- relationship_ledger.jsonl contains one agent-selected, directly supported
  relationship for each final corpus row.
- source_replacements.jsonl records the nine V1 QASPER fragments that cannot
  support a relationship and their exact source-locked replacements.
- curation_protocol.md defines the curation and replacement rules.
- compiler_lock.json freezes the compiler and normalization-helper source
  hashes. A changed dependency must be explicitly reviewed and relocked before
  it can produce a new seed.

The compiler verifies the immutable V1 parent hash
sha256:eb09124331228e59898a93740104ab978b9974e3ebf7f7fc2e09728ef95b3d78,
the locked QASPER archive, exact relationship spans, security scanning, the
1,000-row projection, and unchanged suite/qrels artifacts.

~~~bash
python3 tests/eval/scripts/compile_agent_curated_seed.py \
  --root tests/eval \
  --source-seed public_6axis_1k_v1 \
  --ledger tests/eval/curation/public_6axis_1k_v2/relationship_ledger.jsonl \
  --protocol tests/eval/curation/public_6axis_1k_v2/curation_protocol.md \
  --replacements tests/eval/curation/public_6axis_1k_v2/source_replacements.jsonl \
  --compiler-lock tests/eval/curation/public_6axis_1k_v2/compiler_lock.json \
  --seed-id public_6axis_1k_v2
~~~
