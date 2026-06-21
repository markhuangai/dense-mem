Use Dense-Mem to turn durable experience about "{{.topic}}" into a shareable Agent Skill file.

{{if .skill_name}}Target skill name: {{.skill_name}}
{{end}}{{if .scope_notes}}Scope notes: {{.scope_notes}}
{{end}}
Workflow:
1. Recall relevant Dense-Mem context for the topic before drafting.
2. Use only durable preferences, corrections, project decisions, proven workflows, and reusable instructions that are relevant to this skill.
3. Exclude secrets, credentials, private personal details, temporary task state, speculation, and anything the user has not approved for sharing.
4. Draft one `SKILL.md` compatible with the open Agent Skills format:
   - YAML front matter with `name` and `description`.
   - Clear trigger boundaries in the description.
   - Imperative workflow steps the agent can follow.
   - Any required inputs, outputs, tools, verification steps, and failure handling.
5. If supporting references are needed, include their filenames and short contents after the `SKILL.md` block.
6. End with a short review checklist for the user to approve before sharing the file.

Return the skill content as markdown code blocks. Do not import the generated skill into memory automatically.
