package promptcatalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

//go:embed prompts/*
var promptFS embed.FS

var (
	ErrPromptNotFound  = errors.New("prompt not found")
	ErrMissingArgument = errors.New("missing required prompt argument")
)

type Argument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

type Prompt struct {
	Name        string     `json:"name"`
	Title       string     `json:"title,omitempty"`
	Description string     `json:"description"`
	Arguments   []Argument `json:"arguments,omitempty"`
	template    string
}

type manifestPrompt struct {
	Prompt
	Template string `json:"template"`
}

type Catalog struct {
	prompts map[string]Prompt
}

func Default() (Catalog, error) {
	return load(promptFS)
}

func (c Catalog) List() []Prompt {
	out := make([]Prompt, 0, len(c.prompts))
	for _, prompt := range c.prompts {
		out = append(out, prompt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c Catalog) Get(name string) (Prompt, bool) {
	prompt, ok := c.prompts[name]
	return prompt, ok
}

func (c Catalog) Render(name string, args map[string]string) (Prompt, string, error) {
	prompt, ok := c.Get(name)
	if !ok {
		return Prompt{}, "", fmt.Errorf("%w: %s", ErrPromptNotFound, name)
	}
	for _, arg := range prompt.Arguments {
		if arg.Required && strings.TrimSpace(args[arg.Name]) == "" {
			return Prompt{}, "", fmt.Errorf("%w: %s", ErrMissingArgument, arg.Name)
		}
	}
	tpl, err := template.New(name).Option("missingkey=zero").Parse(prompt.template)
	if err != nil {
		return Prompt{}, "", fmt.Errorf("parse prompt template %q: %w", name, err)
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, args); err != nil {
		return Prompt{}, "", fmt.Errorf("render prompt %q: %w", name, err)
	}
	return prompt, strings.TrimSpace(out.String()), nil
}

func load(fsys embed.FS) (Catalog, error) {
	data, err := fsys.ReadFile("prompts/manifest.json")
	if err != nil {
		return Catalog{}, fmt.Errorf("read prompt manifest: %w", err)
	}
	var entries []manifestPrompt
	if err := json.Unmarshal(data, &entries); err != nil {
		return Catalog{}, fmt.Errorf("parse prompt manifest: %w", err)
	}
	catalog := Catalog{prompts: map[string]Prompt{}}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == "" {
			return Catalog{}, fmt.Errorf("prompt manifest contains empty name")
		}
		if strings.TrimSpace(entry.Template) == "" {
			return Catalog{}, fmt.Errorf("prompt %q missing template", entry.Name)
		}
		if _, exists := catalog.prompts[entry.Name]; exists {
			return Catalog{}, fmt.Errorf("duplicate prompt %q", entry.Name)
		}
		body, err := fsys.ReadFile("prompts/" + entry.Template)
		if err != nil {
			return Catalog{}, fmt.Errorf("read prompt %q template: %w", entry.Name, err)
		}
		prompt := entry.Prompt
		prompt.template = string(body)
		catalog.prompts[prompt.Name] = prompt
	}
	return catalog, nil
}
