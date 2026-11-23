# GitHub Actions workflows

## Find a workflow

| Task | Entry point | Shared work |
| --- | --- | --- |
| Validate changes | `ci-pr.yml` for PRs and `ci-push.yml` for pushes to `main`. `codeql.yml` and `ai-pr-review.yml` run the separate analysis and review gates. | `ci-shared.yml` owns the repository and browser checks. |
| Validate a preview or a pushed development commit | `pr-test-image.yml` owns the admin preview request and reports merge checks. Use `development-e2e.yml` for selected tests on a pushed SHA. Contributors run `fork-e2e-request.yml` in their forks. | `production-image-e2e.yml` runs the full matrix or an authorized manual trial. `fork-e2e.yml` verifies contributor approval and signs the fork receipt. Both and Development E2E call `production-e2e-scenario.yml`. |
| Clean temporary images | `pr-image-cleanup-request.yml` records a manual request. `pr-image-cleanup.yml` consumes it and handles scheduled and PR-close cleanup. `development-e2e-cleanup.yml` handles completed development runs and its recovery sweep. | The cleanup workflows own distinct packages and verify run or PR ownership before deletion. |
| Run evaluation | Use `run-evaluation.yml` for an image tag or the evaluation mode in `production-image-e2e.yml` for your validated PR. | `evaluation.yml` executes the selected dataset campaign. |
| Release and deploy | `release-rc.yml` follows successful main CI. Use `start-prerelease.yml` to start a release line, `release.yml` for stable promotion, and `deploy-stage.yml` for staging deployment. | `publish-image.yml` builds either prerelease image. `publish-python.yml` verifies package artifacts and supports manual recovery. `staging-migration-rehearsal.yml` verifies migrations on a production copy before prerelease publication. |
| Publish documentation | Merge a checked documentation PR to `gh-pages`. | GitHub Pages builds and publishes the branch root. |

## Publish documentation

The [documentation site](https://markhuangai.github.io/dense-mem/) uses the
`gh-pages` branch as its source. Start a separate documentation worktree
from freshly fetched `origin/gh-pages`. Keep application changes in their own
worktree from `origin/main` and synchronize each PR with its matching base.

For documented behavior changes, link the non-draft site PR and feature PR to
each other. Cite the application revision the site explains and distinguish
implemented behavior from release or deployment availability. A documentation
change by itself can ship in a single site PR. The feature PR retains the
applicable product review, CI, E2E, and evaluation gates.

Use the `gh-pages` branch's contributor README and **Documentation site**
check. Build with the GitHub Pages Jekyll stack and verify generated URLs,
anchors, affected examples, and browser interactions under `/dense-mem/`.
Verify the documented implementation references and rerun the site checks for
the final PR head. Keep one explanation per subject and reuse shared data in
the animations.

GitHub Pages publishes from **Settings → Pages → Deploy from a branch →
`gh-pages` → `/ (root)`**. After a site merge, verify that the **Pages build
and deployment** workflow succeeded for that `gh-pages` commit, then open the
published routes and deep links. For a failure, inspect the run for the source
commit and build or deployment error, fix the source through a documentation
PR, and verify its resulting deployment. A PR check proves a candidate build.
The post-merge run and rendered site establish publication.
When an application PR replaces existing documentation links, publish and
verify the new destinations before merging that application PR.

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

For validation and staging procedures, see [Development And Testing](https://markhuangai.github.io/dense-mem/Development-And-Testing/) and the [Operations Runbook](https://markhuangai.github.io/dense-mem/Operations-Runbook/).
