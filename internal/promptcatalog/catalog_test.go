package promptcatalog

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultCatalogListsExportMemoryAsAgentSkill(t *testing.T) {
	catalog, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	prompts := catalog.List()
	if len(prompts) != 1 {
		t.Fatalf("prompts len = %d, want 1", len(prompts))
	}
	if prompts[0].Name != "export_memory_as_agent_skill" {
		t.Fatalf("prompt name = %q", prompts[0].Name)
	}
	if len(prompts[0].Arguments) == 0 || prompts[0].Arguments[0].Name != "topic" || !prompts[0].Arguments[0].Required {
		t.Fatalf("arguments = %+v", prompts[0].Arguments)
	}
}

func TestDefaultCatalogRenderValidatesAndRenders(t *testing.T) {
	catalog, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if _, _, err := catalog.Render("export_memory_as_agent_skill", map[string]string{}); !errors.Is(err, ErrMissingArgument) || !strings.Contains(err.Error(), "topic") {
		t.Fatalf("missing topic err = %v, want ErrMissingArgument", err)
	}
	if _, _, err := catalog.Render("missing", map[string]string{"topic": "incident response"}); !errors.Is(err, ErrPromptNotFound) {
		t.Fatalf("missing prompt err = %v, want ErrPromptNotFound", err)
	}
	_, text, err := catalog.Render("export_memory_as_agent_skill", map[string]string{
		"topic":       "incident response",
		"skill_name":  "incident-response",
		"scope_notes": "internal runbooks only",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"incident response",
		"incident-response",
		"internal runbooks only",
		"SKILL.md",
		"self-contained",
		"recipients who do not have access",
		"Do not tell the generated skill to query Dense-Mem",
		"portability and privacy checks",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered text missing %q: %s", want, text)
		}
	}
}

func TestCatalogListSortsByName(t *testing.T) {
	catalog := Catalog{prompts: map[string]Prompt{
		"zeta":  {Name: "zeta"},
		"alpha": {Name: "alpha"},
	}}

	prompts := catalog.List()

	if len(prompts) != 2 {
		t.Fatalf("prompts len = %d, want 2", len(prompts))
	}
	if prompts[0].Name != "alpha" || prompts[1].Name != "zeta" {
		t.Fatalf("prompts order = %q, %q; want alpha, zeta", prompts[0].Name, prompts[1].Name)
	}
}

func TestCatalogRenderTemplateParseError(t *testing.T) {
	catalog := Catalog{prompts: map[string]Prompt{
		"bad": {Name: "bad", template: "{{if"},
	}}

	_, _, err := catalog.Render("bad", map[string]string{})

	if err == nil || !strings.Contains(err.Error(), `parse prompt template "bad"`) {
		t.Fatalf("Render error = %v, want parse prompt template error", err)
	}
}
