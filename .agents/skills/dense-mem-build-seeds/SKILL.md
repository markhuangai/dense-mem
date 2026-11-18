---
name: dense-mem-build-seeds
description: Build and publish complete Dense-Mem public evaluation PostgreSQL seeds with local Docker. Use for seed preparation or rebuilding a named dataset. Scored evaluations run in GitHub Actions.
---

# Build Dense-Mem evaluation seeds

1. Read [the evaluation guide](../../../tests/eval/README.md). Verify the checkout and its CI. For a pre-merge build, use the current validated head of the maintainer's own PR and pass its number.
2. Confirm Docker can access GHCR with package write permission and the five dedicated evaluation settings are available from a private file or the environment. Keep values out of commands, output, and published images.
3. Run from the repository root:

   ```bash
   node tests/eval/scripts/local-seeds.mjs --pr <PR_NUMBER> --workers 20
   ```

   Omit `--pr` on current main to use its published release. Workers default to 20 and accept integers from 1 to 100. All five datasets build serially by default. Use `--dataset <DATASET>` for one pack. For file-backed settings, pass `--env-file=<PRIVATE_FILE>` to Node before the script.
4. Monitor the minute-by-minute document counts and resource samples. Verify that every requested dataset has a dated tag, a matching `latest` alias, and a digest receipt. On failure, retain the protected error and report the dataset and completed count. A new build requires explicit direction.
