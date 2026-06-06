# Dense-Mem Review Context

Use Dense-Mem project memory as review context when prior decisions, previous review findings, CI history, or implementation rationale may affect the pull request. Treat memory as context, not as proof by itself.

Use the read-only Dense-Mem MCP tools available to this stage whenever repository memory would reduce uncertainty. Prefer `assemble_context` for bounded project context and `recall_memory` for targeted follow-up.

Focus Dense-Mem lookups on durable repository decisions, prior review findings, CI failures, security constraints, MCP/tooling behavior, and implementation rationale that may affect the current pull request.

Do not report a finding from Dense-Mem memory alone. Verify every blocking issue against the pull request diff, repository files, tests, logs, or GitHub context, and cite that concrete evidence in the review output.
