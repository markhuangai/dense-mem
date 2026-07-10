package neo4j

import "testing"

// TestFragmentActiveFilter_AdmitsOnlyActive locks the security boundary for
// quarantined, retracted, and future non-active states.
func TestFragmentActiveFilter_AdmitsOnlyActive(t *testing.T) {
	const want = "coalesce(sf.status,'active') = 'active'"
	if FragmentActiveFilter != want {
		t.Errorf("FragmentActiveFilter = %q; want %q", FragmentActiveFilter, want)
	}
}
