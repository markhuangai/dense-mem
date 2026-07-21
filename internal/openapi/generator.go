package openapi

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

// SpecVariant distinguishes the public AI-safe spec from the full runtime spec.
type SpecVariant string

const (
	SpecVariantAISafe SpecVariant = "ai-safe"
	SpecVariantFull   SpecVariant = "full"
)

// BuildInfo is injected at construction time so the spec's info block reflects
// the live deployment instead of hard-coded strings.
type BuildInfo struct {
	Title       string
	Version     string
	Description string
}

// DefaultBuildInfo returns a safe default if no build info is wired.
func DefaultBuildInfo() BuildInfo {
	return BuildInfo{
		Title:       "dense-mem",
		Version:     "v1",
		Description: "Multi-profile memory service for AI agents.",
	}
}

// Generator produces OpenAPI 3.0.3 documents.
type Generator interface {
	Generate(variant SpecVariant) (map[string]any, error)
}

type generator struct {
	reg    registry.Registry
	routes []RouteDescriptor
	info   BuildInfo
}

var _ Generator = (*generator)(nil)

// New constructs a Generator bound to a live tool registry and route table.
func New(reg registry.Registry, routes []RouteDescriptor) Generator {
	return &generator{reg: reg, routes: routes, info: DefaultBuildInfo()}
}

// NewWithInfo constructs a Generator with a custom build info block.
func NewWithInfo(reg registry.Registry, routes []RouteDescriptor, info BuildInfo) Generator {
	return &generator{reg: reg, routes: routes, info: info}
}

// Generate returns an OpenAPI 3.0.3 document tailored to the requested variant.
func (g *generator) Generate(variant SpecVariant) (map[string]any, error) {
	if variant != SpecVariantAISafe && variant != SpecVariantFull {
		return nil, fmt.Errorf("openapi: unknown variant %q", variant)
	}
	if g.reg == nil {
		return nil, errors.New("openapi: registry is required")
	}

	paths := map[string]any{}
	schemas := baseSchemas()
	// Merge knowledge-domain schemas so routes that ship before MCP tool
	// registration can reference them via explicit RequestSchema/ResponseSchema.
	for k, v := range knowledgeSchemas() {
		schemas[k] = v
	}

	for _, route := range g.routes {
		if !routeMatches(route, variant) {
			continue
		}
		if route.ToolName != "" {
			if _, ok := g.reg.Get(route.ToolName); !ok {
				continue
			}
		}
		op := g.buildOperation(route, schemas)
		pathItem, ok := paths[route.Path].(map[string]any)
		if !ok {
			pathItem = map[string]any{}
			paths[route.Path] = pathItem
		}
		pathItem[strings.ToLower(route.Method)] = op
	}

	// Fold tool input/output schemas from the registry into components.schemas
	// so operations can $ref them (AC-34 derived-from-registry).
	for _, t := range g.reg.List() {
		if registry.IsV2ContractTool(t) {
			schemas["DenseMemV2Error"] = cloneSchema(registry.V2PublicErrorSchema())
		}
		if t.InputSchema != nil {
			schemas[schemaNameFor(t.Name, "Input")] = cloneSchema(t.InputSchema)
		}
		if t.OutputSchema != nil {
			schemas[schemaNameFor(t.Name, "Output")] = cloneSchema(t.OutputSchema)
		}
	}

	doc := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       g.info.Title,
			"version":     g.info.Version,
			"description": g.info.Description,
		},
		"servers": []map[string]any{
			{"url": "/", "description": "Current host"},
		},
		"components": map[string]any{
			"securitySchemes": securitySchemes(),
			"schemas":         schemas,
		},
		"security": []map[string]any{
			{"BearerAuth": []any{}},
		},
		"paths": paths,
	}
	return doc, nil
}

func routeMatches(r RouteDescriptor, variant SpecVariant) bool {
	switch variant {
	case SpecVariantAISafe:
		return r.AISafe
	case SpecVariantFull:
		return true
	default:
		return false
	}
}

// buildOperation composes a single OpenAPI operation for a route.
//
// Schema resolution priority (highest to lowest):
//  1. Explicit RequestSchema / ResponseSchema on the RouteDescriptor.
//  2. ToolName-derived schemas pulled from the registry.
//
// This allows knowledge-pipeline routes to ship with explicit schema refs
// before their MCP tool counterparts are registered.
func (g *generator) buildOperation(r RouteDescriptor, schemas map[string]any) map[string]any {
	// Determine the success status code. Default 200; creation routes use 201.
	successStatus := "200"
	if r.SuccessStatus != 0 {
		successStatus = fmt.Sprintf("%d", r.SuccessStatus)
	}

	responses := map[string]any{
		successStatus: map[string]any{"description": "OK"},
		"400":         errorResponse("Validation error."),
		"401":         errorResponse("Missing or invalid credentials."),
		"403":         errorResponse("Forbidden."),
		"404":         errorResponse("Not found."),
		"429":         errorResponse("Rate limit exceeded."),
		"500":         errorResponse("Internal error."),
	}
	// Merge caller-supplied extra responses (e.g. 202 Accepted for async routes).
	for code, desc := range r.ExtraResponses {
		responses[code] = errorResponse(desc)
	}
	if tool, ok := g.reg.Get(r.ToolName); ok && registry.IsV2ContractTool(tool) {
		for _, code := range []string{"400", "401", "403", "404", "429", "500"} {
			description := responses[code].(map[string]any)["description"].(string)
			responses[code] = errorResponseWithSchema(description, "DenseMemV2Error")
		}
		for code, desc := range r.ExtraResponses {
			responses[code] = errorResponseWithSchema(desc, "DenseMemV2Error")
		}
	}

	op := map[string]any{
		"operationId": r.OperationID,
		"summary":     firstLine(r.Description),
		"description": r.Description,
		"tags":        tagsFor(r),
		"responses":   responses,
	}

	// Path parameters — derived from {name} segments.
	if params := pathParams(r.Path); len(params) > 0 {
		op["parameters"] = params
	}

	// Resolve request and response schema refs. Explicit fields win; fall back
	// to the tool registry when ToolName is set.
	reqSchemaRef := r.RequestSchema
	respSchemaRef := r.ResponseSchema

	if (reqSchemaRef == "" || respSchemaRef == "") && r.ToolName != "" {
		if tool, ok := g.reg.Get(r.ToolName); ok {
			if reqSchemaRef == "" && tool.InputSchema != nil {
				reqSchemaRef = schemaNameFor(tool.Name, "Input")
			}
			if respSchemaRef == "" && tool.OutputSchema != nil {
				respSchemaRef = schemaNameFor(tool.Name, "Output")
			}
		}
	}

	if reqSchemaRef != "" && (r.Method == "POST" || r.Method == "PATCH" || r.Method == "PUT") {
		op["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"$ref": "#/components/schemas/" + reqSchemaRef,
					},
				},
			},
		}
	}

	if respSchemaRef != "" {
		responses[successStatus] = map[string]any{
			"description": "Success",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"$ref": "#/components/schemas/" + respSchemaRef,
					},
				},
			},
		}
	}

	return op
}

func pathParams(p string) []map[string]any {
	var out []map[string]any
	segs := strings.Split(p, "/")
	names := make([]string, 0)
	for _, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}"))
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, map[string]any{
			"name":     n,
			"in":       "path",
			"required": true,
			"schema":   map[string]any{"type": "string"},
		})
	}
	return out
}

func tagsFor(r RouteDescriptor) []string {
	// Explicit Tags on the descriptor take priority over path inference.
	if len(r.Tags) > 0 {
		return r.Tags
	}
	switch {
	case strings.Contains(r.Path, "/fragments"):
		return []string{"fragments"}
	case strings.Contains(r.Path, "/tools"):
		return []string{"tools"}
	case strings.Contains(r.Path, "/teams"):
		return []string{"teams"}
	case strings.Contains(r.Path, "/profiles"):
		return []string{"profiles"}
	default:
		return []string{"api"}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i]
	}
	return s
}

func securitySchemes() map[string]any {
	return map[string]any{
		"BearerAuth": map[string]any{
			"type":         "http",
			"scheme":       "bearer",
			"bearerFormat": "API key",
		},
	}
}

func errorResponse(description string) map[string]any {
	return errorResponseWithSchema(description, "ErrorResponse")
}

func errorResponseWithSchema(description string, schemaName string) map[string]any {
	return map[string]any{
		"description": description,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"$ref": "#/components/schemas/" + schemaName,
				},
			},
		},
	}
}

// baseSchemas returns the fixed component schemas present in every spec variant.
// The ErrorResponse shape matches httperr.ErrorEnvelope exactly:
//
//	{ "error": { "code": string, "message": string, "details": [...] } }
func baseSchemas() map[string]any {
	return map[string]any{
		"ErrorResponse": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"error": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code":    map[string]any{"type": "string"},
						"message": map[string]any{"type": "string"},
						"details": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"field":   map[string]any{"type": "string"},
									"message": map[string]any{"type": "string"},
								},
								"required": []string{"field", "message"},
							},
						},
					},
					"required": []string{"code", "message"},
				},
			},
			"required": []string{"error"},
		},
	}
}

// schemaNameFor produces a stable component-schema name for a tool's
// input/output. Underscores and hyphens in tool names are elided.
func schemaNameFor(tool, suffix string) string {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case '_', '-':
			return -1
		default:
			return r
		}
	}, tool)
	// Capitalize first letter.
	if clean == "" {
		return suffix
	}
	return strings.ToUpper(clean[:1]) + clean[1:] + suffix
}

// cloneSchema deep-copies a map so callers can't mutate the generator's
// components block by editing the returned doc.
func cloneSchema(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch vv := v.(type) {
		case map[string]any:
			out[k] = cloneSchema(vv)
		case []any:
			out[k] = cloneSlice(vv)
		default:
			out[k] = v
		}
	}
	return out
}

func cloneSlice(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		switch vv := v.(type) {
		case map[string]any:
			out[i] = cloneSchema(vv)
		case []any:
			out[i] = cloneSlice(vv)
		default:
			out[i] = v
		}
	}
	return out
}
