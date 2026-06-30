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
