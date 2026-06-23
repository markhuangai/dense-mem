Use Dense-Mem to turn durable experience about "{{.topic}}" into a self-contained, shareable Agent Skill file.

{{if .skill_name}}Target skill name: {{.skill_name}}
{{end}}{{if .scope_notes}}Scope notes: {{.scope_notes}}
{{end}}
Workflow:
1. Recall relevant Dense-Mem context for the topic before drafting.
2. Distill the recalled context into concrete rules, workflows, examples, constraints, validation steps, and failure handling that can live inside the skill.
3. Make the generated skill portable for recipients who do not have access to the source Dense-Mem instance or MCP server.
4. Do not tell the generated skill to query Dense-Mem, a memory MCP, or the source memory store during normal execution. Mention source memory only as optional provenance or review context outside the skill content.
5. Use only durable preferences, corrections, project decisions, proven workflows, and reusable instructions that are relevant to this skill.
6. Exclude secrets, credentials, private personal details, temporary task state, speculation, and anything the user has not approved for sharing.
7. Draft one `SKILL.md` compatible with the open Agent Skills format:
   - YAML front matter with `name` and `description`.
   - Clear trigger boundaries in the description.
   - Imperative workflow steps the agent can follow.
   - Any required inputs, outputs, tools, verification steps, and failure handling.
   - Enough embedded detail that the skill remains useful without private memory access.
8. If supporting references are needed, include their filenames and complete shareable contents after the `SKILL.md` block.
9. End with a short review checklist for the user to approve before sharing the file, including portability and privacy checks.

Return the skill content as markdown code blocks. Do not import the generated skill into memory automatically.
