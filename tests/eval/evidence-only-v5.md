# Evidence-only v5 matched campaign

A seed corpus row is plain evidence: `source_doc_id`, `content`, and optional
source metadata. Content is split rather than truncated, and the Go harness
rejects any corpus row above 999 Unicode code points. Legacy `claims` and
`auto_promote` fields are rejected because they bypass exact Evidence admission
and synchronous Remember. Retired `relationships` and graph proposal fields are
rejected before provider work.

Campaign `issue_681_evidence_only_v5` reuses C's sealed native capture and stored
terminal answers without starting its retired runtime. It retains all 991 source
rows in dispatch order, 204 retrieval cases and qrels, and 82 answer references
per policy. Preserve ordinary seed validation and source provenance. Use the
paired runner's explicit `retained_baseline` and `evidence_only_v5` modes.

Freeze manifests, prompts, rubric, scorer, runner/source/image identities, and
effective settings before execution. Rescore C's stored answers before inspecting
candidate answers. Pin `dense-mem-testing`, `@cf/baai/bge-m3`, 1,024 embedding
dimensions, `vector_hnsw`, and intake concurrency four. Keep filled configuration,
credentials, seeds, and all generated artifacts in ignored paths.

```bash
PAIR_CONFIG="$PWD/tests/eval/runs/evidence-only-v5/pair.json" \
  tests/eval/scripts/run_full_public_rag_eval_until_done.sh
```

The campaign requires Recall@10 >= 0.893545751633987, nDCG@10 >=
0.8328513920765662, MRR >= 0.8511223934018051, complete support >= 174/204,
and zero unmapped references. Each answer policy must separately meet rescored
C accuracy, 100% quotation and claim-support correctness, and cited-answer
coverage >= 77/82. One-call uses exactly one memory call; iterative uses at most
eight. Performance is report-only under issue #731's recorded waiver.

The approved campaign has one 24-hour budget, including preparation, with the
final 30 minutes reserved for preservation and cleanup. Do not restart or extend
it. Each failed intake permits one identity-preserving supported recovery;
retain every original outcome and unavailable measurement. Retain C's incomplete
historical organization coverage. Runtime or evaluator changes invalidate
affected quality evidence. Require native paired `--pair-action report`
reproduction without a runtime. Only a complete measured result replaces
`baseline.json`.
