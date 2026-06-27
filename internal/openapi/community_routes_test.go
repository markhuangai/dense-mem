package openapi

import "testing"

func TestGenerateOmitsDirectCommunityReadRoutes(t *testing.T) {
	g := New(testRegistry(t), DefaultRoutes())

	for _, variant := range []SpecVariant{SpecVariantAISafe, SpecVariantFull} {
		spec, err := g.Generate(variant)
		if err != nil {
			t.Fatalf("Generate(%s): %v", variant, err)
		}

		paths := spec["paths"].(map[string]any)
		for _, path := range []string{"/api/v1/communities", "/api/v1/communities/{id}"} {
			if _, ok := paths[path]; ok {
				t.Fatalf("%s route %q must not be exposed", variant, path)
			}
		}
	}
}
