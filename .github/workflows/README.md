# GitHub Actions workflows

## Find a workflow

| Task | Entry point | Shared work |
| --- | --- | --- |
| Validate changes | `ci-pr.yml` for PRs and `ci-push.yml` for pushes to `main`. `codeql.yml` and `ai-pr-review.yml` run the separate analysis and review gates. | `ci-shared.yml` owns the repository and browser checks. |
| Validate a preview or a pushed development commit | `pr-test-image.yml` owns the admin preview request and reports merge checks. Use `development-e2e.yml` for selected tests on a pushed SHA. Contributors run `fork-e2e-request.yml` in their forks. | `production-image-e2e.yml` runs the full matrix or an authorized manual trial. `fork-e2e.yml` verifies contributor approval and signs the fork receipt. Both and Development E2E call `production-e2e-scenario.yml`. |
| Clean temporary images | `pr-image-cleanup-request.yml` records a manual request. `pr-image-cleanup.yml` consumes it and handles scheduled and PR-close cleanup. `development-e2e-cleanup.yml` handles completed development runs and its recovery sweep. | The cleanup workflows own distinct packages and verify run or PR ownership before deletion. |
| Run evaluation | Use `run-evaluation.yml` for an image tag or the evaluation mode in `production-image-e2e.yml` for your validated PR. | `evaluation.yml` executes the selected dataset campaign. |
| Release and deploy | `release-rc.yml` follows successful main CI. Use `start-prerelease.yml` to start a release line, `release.yml` for stable promotion, and `deploy-stage.yml` for staging deployment. | `publish-image.yml` builds either prerelease image. `publish-python.yml` verifies package artifacts and supports manual recovery. `staging-migration-rehearsal.yml` verifies migrations on a production copy before prerelease publication. |
| Publish the wiki | `wiki-source.yml` on the `wiki` branch signals `publish-wiki.yml` on `main`. | `publish-wiki.yml` also supports manual recovery. |

## Publish a prerelease image

The automatic prerelease workflow publishes the demo image and either promotes
the verified production preview or rebuilds production. To rebuild an existing
tag manually, dispatch **Publish prerelease Docker image** from `main`.

```bash
gh workflow run publish-image.yml --repo markhuangai/dense-mem --ref main \
  -f tag='<existing-prerelease-tag>'

gh workflow run publish-image.yml --repo markhuangai/dense-mem --ref main \
  -f tag='<existing-prerelease-tag>' -f variant=demo
```

Production publishes the version tag. Demo publishes the same version with a
`demo-` prefix. The second command replaces the former demo image workflow.

For validation and staging procedures, see [Development And Testing](https://github.com/markhuangai/dense-mem/wiki/Development-And-Testing) and the [Operations Runbook](https://github.com/markhuangai/dense-mem/wiki/Operations-Runbook).
