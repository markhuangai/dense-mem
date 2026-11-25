#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

node --test \
  tests/uat/ai_pr_review_policy.test.mjs \
  tests/uat/coverage_policy.test.mjs \
  tests/uat/decision_providers/deployment.test.mjs \
  tests/uat/development_e2e.test.mjs \
  tests/uat/e2e_host_controller.test.mjs \
  tests/uat/e2e_scenario_registry.test.mjs \
  tests/uat/embedding_upstream.test.mjs \
  tests/uat/fork_attestation_crypto.test.mjs \
  tests/uat/fork_e2e_policy.test.mjs \
  tests/uat/fork_e2e_workspace.test.mjs \
  tests/uat/github_cleanup_api.test.mjs \
  tests/uat/go_vulnerability_scan_policy.test.mjs \
  tests/uat/image_release_policy.test.mjs \
  tests/uat/npm_vulnerability_scan_policy.test.mjs \
  tests/uat/ontology_organization_cleanup.test.mjs \
  tests/uat/ontology_semantic_diagnostics.test.mjs \
  tests/uat/pr_image_cleanup.test.mjs \
  tests/uat/pr_validation_policy.test.mjs \
  tests/uat/prerelease_version.test.mjs \
  tests/uat/preview_receipt.test.mjs \
  tests/uat/synchronous_write/progress.test.mjs \
  tests/uat/synchronous_write/remember_retry.test.mjs \
  tests/uat/synchronous_write/runner.test.mjs \
  tests/uat/synchronous_write/timing.test.mjs
