---
name: dense-mem-build-seeds
description: Build and publish complete Dense-Mem public evaluation PostgreSQL seeds with ontology disabled using local Docker. Use for seed preparation or rebuilding a named dataset. Scored evaluations run in GitHub Actions.
---

# Build Dense-Mem evaluation seeds

1. Read [the evaluation guide](../../../tests/eval/README.md). Verify the checkout and its CI. For a pre-merge build, use the current validated head of the maintainer's own PR and pass its number.
2. Confirm Docker can access GHCR with package write permission and the five dedicated evaluation settings are available from a private file or the environment. Keep values out of commands, output, and published images.
3. Run from the repository root:

   ```bash
   node tests/eval/scripts/local-seeds.mjs --pr <PR_NUMBER> --workers 20
   ```

   Omit `--pr` on current main to use its published release. Workers default to 20 and accept integers from 1 to 100. All five datasets build serially by default. Use `--dataset <DATASET>` for one pack. Use `--skip-ai-security-check` when the requested build calls for the public corpus admission policy. For file-backed settings, pass `--env-file=<PRIVATE_FILE>` to Node before the script.
4. Monitor minute-by-minute document counts and resource samples. A confirmed HTTP 429 pauses new imports for 60 seconds before bounded retry. Each dataset must boot its seeded PostgreSQL image on fresh storage and verify ontology is disabled and paused, canonical Evidence and source mappings match, and search is converged before publication. Verify its dated tag, `latest` alias and digest receipt.
5. On failure, report the dataset, completed count and build ID. To resume on the same Docker daemon, use `node tests/eval/scripts/local-seeds.mjs --resume <build-id>` with the original trusted checkout and credentials. Resume uses the saved settings and verifies committed rows before importing the remainder. A new invocation requires explicit direction.
