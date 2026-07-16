package neo4j

import "testing"

// TestFragmentActiveFilter_ExcludesRetracted verifies that the FragmentActiveFilter
// constant holds the exact Cypher expression required by AC-44: coalesce-guarded
// exclusion of retracted SourceFragment nodes, treating missing status as active.
func TestFragmentActiveFilter_ExcludesRetracted(t *testing.T) {
	const want = "coalesce(sf.status,'active') <> 'retracted'"
	if FragmentActiveFilter != want {
		t.Errorf("FragmentActiveFilter = %q; want %q", FragmentActiveFilter, want)
	}
}
