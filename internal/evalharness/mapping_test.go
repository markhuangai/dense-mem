package evalharness

import "testing"

func TestMapExpectedDreamsUsesStableDreamIDOrder(t *testing.T) {
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-employer", SourceDocID: "doc-employer"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-location", SourceDocID: "doc-location"}, false)
	addDreamSourceRefs(&mapping, "dream-zeta", []Ref{
		{Type: "fact", ID: "fact-location"},
		{Type: "fact", ID: "fact-employer"},
	})
	addDreamSourceRefs(&mapping, "dream-alpha", []Ref{
		{Type: "fact", ID: "fact-employer"},
		{Type: "fact", ID: "fact-location"},
	})

	mapExpectedDreams(&mapping, []ExpectedDream{{
		SourceDocID: "expected-dream",
		SourceRefs: []Ref{
			{Type: "fact", SourceDocID: "doc-employer"},
			{Type: "fact", SourceDocID: "doc-location"},
		},
	}})

	resolved, ok := resolveRef(Ref{Type: "dream", SourceDocID: "expected-dream"}, mapping)
	if !ok || resolved.ID != "dream-alpha" {
		t.Fatalf("resolved dream = %+v, %v; want dream-alpha", resolved, ok)
	}
}

func TestExpectedDreamCycleSeedsResolveSourceDocs(t *testing.T) {
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-employer", SourceDocID: "doc-employer"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-location", SourceDocID: "doc-location"}, false)

	seeds := expectedDreamCycleSeeds(mapping, []ExpectedDream{
		{
			SourceDocID: "expected-dream",
			Hypothesis:  "Employment may explain the location period.",
			SourceRefs: []Ref{
				{Type: "fact", SourceDocID: "doc-employer"},
				{Type: "fact", SourceDocID: "doc-location"},
			},
		},
		{
			SourceDocID: "missing-source",
			Hypothesis:  "This seed cannot be materialized.",
			SourceRefs: []Ref{
				{Type: "fact", SourceDocID: "doc-missing"},
			},
		},
		{
			SourceDocID: "empty-hypothesis",
			SourceRefs: []Ref{
				{Type: "fact", SourceDocID: "doc-employer"},
			},
		},
	})

	if len(seeds) != 1 {
		t.Fatalf("seeds = %+v; want one resolved seed", seeds)
	}
	if seeds[0].Hypothesis != "Employment may explain the location period." {
		t.Fatalf("seed hypothesis = %q", seeds[0].Hypothesis)
	}
	if len(seeds[0].SourceRefs) != 2 || seeds[0].SourceRefs[0].ID != "fact-employer" || seeds[0].SourceRefs[1].ID != "fact-location" {
		t.Fatalf("seed refs = %+v", seeds[0].SourceRefs)
	}
}
