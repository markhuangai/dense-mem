package classification

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLattice covers AC-18 and AC-19: dimension-wise max behaviour, missing-key
// defaulting to minimum, and passthrough of unknown dimensions.
func TestLattice(t *testing.T) {
	t.Parallel()

	l := DefaultLattice()

	t.Run("LatticeVersion is v1", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, "v1", LatticeVersion)
	})

	t.Run("DefaultLattice returns non-nil Lattice", func(t *testing.T) {
		t.Parallel()
		require.NotNil(t, l)
	})

	t.Run("identical maps return the same map", func(t *testing.T) {
		t.Parallel()
		a := map[string]string{
			"confidentiality": "internal",
			"retention":       "short_term",
			"pii":             "none",
		}
		b := map[string]string{
			"confidentiality": "internal",
			"retention":       "short_term",
			"pii":             "none",
		}
		got := l.Max(a, b)
		require.Equal(t, "internal", got["confidentiality"])
		require.Equal(t, "short_term", got["retention"])
		require.Equal(t, "none", got["pii"])
	})

	t.Run("dimension-wise max picks higher rank per dimension", func(t *testing.T) {
		t.Parallel()
		a := map[string]string{
			"confidentiality": "public",
			"retention":       "long_term",
			"pii":             "sensitive",
		}
		b := map[string]string{
			"confidentiality": "restricted",
			"retention":       "ephemeral",
			"pii":             "none",
		}
		got := l.Max(a, b)
		// confidentiality: restricted > public
		require.Equal(t, "restricted", got["confidentiality"])
		// retention: long_term > ephemeral
		require.Equal(t, "long_term", got["retention"])
		// pii: sensitive > none
		require.Equal(t, "sensitive", got["pii"])
	})

	t.Run("full order confidentiality", func(t *testing.T) {
		t.Parallel()
		order := []string{"public", "internal", "confidential", "restricted"}
		for i := 0; i < len(order); i++ {
			for j := i; j < len(order); j++ {
				a := map[string]string{"confidentiality": order[i]}
				b := map[string]string{"confidentiality": order[j]}
				got := l.Max(a, b)
				require.Equal(t, order[j], got["confidentiality"],
					"max(%q, %q) should be %q", order[i], order[j], order[j])
				// commutativity
				got2 := l.Max(b, a)
				require.Equal(t, order[j], got2["confidentiality"],
					"max(%q, %q) commutativity failed", order[j], order[i])
			}
		}
	})

	t.Run("full order retention", func(t *testing.T) {
		t.Parallel()
		order := []string{"ephemeral", "short_term", "long_term", "permanent"}
		for i := 0; i < len(order); i++ {
			for j := i; j < len(order); j++ {
				a := map[string]string{"retention": order[i]}
				b := map[string]string{"retention": order[j]}
				got := l.Max(a, b)
				require.Equal(t, order[j], got["retention"],
					"max(%q, %q) should be %q", order[i], order[j], order[j])
			}
		}
	})

	t.Run("full order pii", func(t *testing.T) {
		t.Parallel()
		order := []string{"none", "non_sensitive", "sensitive"}
		for i := 0; i < len(order); i++ {
			for j := i; j < len(order); j++ {
				a := map[string]string{"pii": order[i]}
				b := map[string]string{"pii": order[j]}
				got := l.Max(a, b)
				require.Equal(t, order[j], got["pii"],
					"max(%q, %q) should be %q", order[i], order[j], order[j])
			}
		}
	})

	t.Run("missing known key defaults to minimum", func(t *testing.T) {
		t.Parallel()
		a := map[string]string{
			// confidentiality absent
			"retention": "long_term",
		}
		b := map[string]string{
			"confidentiality": "internal",
			// retention absent
		}
		got := l.Max(a, b)
		// confidentiality: a missing (=> public), b = internal → max = internal
		require.Equal(t, "internal", got["confidentiality"])
		// retention: a = long_term, b missing (=> ephemeral) → max = long_term
		require.Equal(t, "long_term", got["retention"])
		// pii: both absent → minimum
		require.Equal(t, "none", got["pii"])
	})

	t.Run("empty input maps produce all-minimum result", func(t *testing.T) {
		t.Parallel()
		got := l.Max(map[string]string{}, map[string]string{})
		require.Equal(t, "public", got["confidentiality"])
		require.Equal(t, "ephemeral", got["retention"])
		require.Equal(t, "none", got["pii"])
	})

	t.Run("unknown keys passthrough unchanged", func(t *testing.T) {
		t.Parallel()
		a := map[string]string{
			"confidentiality": "internal",
			"custom_dim":      "alpha",
		}
		b := map[string]string{
			"confidentiality": "public",
			"another_dim":     "beta",
		}
		got := l.Max(a, b)
		require.Equal(t, "internal", got["confidentiality"])
		require.Equal(t, "alpha", got["custom_dim"], "unknown key from a should be present")
		require.Equal(t, "beta", got["another_dim"], "unknown key from b should be present")
	})

	t.Run("unknown key in both inputs uses b value as stable tie-break", func(t *testing.T) {
		t.Parallel()
		a := map[string]string{"custom_dim": "alpha"}
		b := map[string]string{"custom_dim": "beta"}
		got := l.Max(a, b)
		require.Equal(t, "beta", got["custom_dim"])
	})

	t.Run("Max does not mutate input maps", func(t *testing.T) {
		t.Parallel()
		a := map[string]string{"confidentiality": "public"}
		b := map[string]string{"confidentiality": "restricted"}
		aCopy := map[string]string{"confidentiality": "public"}
		bCopy := map[string]string{"confidentiality": "restricted"}
		l.Max(a, b)
		require.Equal(t, aCopy, a, "input map a was mutated")
		require.Equal(t, bCopy, b, "input map b was mutated")
	})

	t.Run("Max returns a new map allocation each call", func(t *testing.T) {
		t.Parallel()
		a := map[string]string{"confidentiality": "public"}
		b := map[string]string{"confidentiality": "restricted"}
		got1 := l.Max(a, b)
		got2 := l.Max(a, b)
		// Modify got1 — got2 must not be affected.
		got1["confidentiality"] = "internal"
		require.Equal(t, "restricted", got2["confidentiality"])
	})

	t.Run("associativity: Max(Max(a,b),c) == Max(a,Max(b,c))", func(t *testing.T) {
		t.Parallel()
		a := map[string]string{"confidentiality": "public", "retention": "permanent", "pii": "none"}
		b := map[string]string{"confidentiality": "confidential", "retention": "short_term", "pii": "sensitive"}
		c := map[string]string{"confidentiality": "internal", "retention": "long_term", "pii": "non_sensitive"}

		lhs := l.Max(l.Max(a, b), c)
		rhs := l.Max(a, l.Max(b, c))

		for dim := range dimensionOrder {
			require.Equal(t, lhs[dim], rhs[dim], "associativity failed for dimension %q", dim)
		}
	})
}

// TestLattice_CrossProfileIsolation covers the profile-isolation requirement:
// the Lattice is a stateless pure function — computing classifications for
// profile A must not affect any subsequent computation for profile B, and vice
// versa. This test verifies that no state leaks between logical "profile A"
// and "profile B" classification sets.
func TestLattice_CrossProfileIsolation(t *testing.T) {
	t.Parallel()

	l := DefaultLattice()

	// Simulate profile A supplying a highly sensitive classification.
	profileALabels := map[string]string{
		"confidentiality": "restricted",
		"retention":       "permanent",
		"pii":             "sensitive",
	}

	// Profile B supplies minimum-sensitivity labels.
	profileBLabels := map[string]string{
		"confidentiality": "public",
		"retention":       "ephemeral",
		"pii":             "none",
	}

	// Compute max for profile A.
	aResult := l.Max(profileALabels, profileALabels)

	// Compute max for profile B.
	bResult := l.Max(profileBLabels, profileBLabels)

	// Profile B result must NOT contain profile A's sensitive values.
	require.Equal(t, "public", bResult["confidentiality"],
		"profile B confidentiality must not be elevated by profile A computation")
	require.Equal(t, "ephemeral", bResult["retention"],
		"profile B retention must not be elevated by profile A computation")
	require.Equal(t, "none", bResult["pii"],
		"profile B pii must not be elevated by profile A computation")

	// Profile A result is unaffected by the profile B computation.
	require.Equal(t, "restricted", aResult["confidentiality"])
	require.Equal(t, "permanent", aResult["retention"])
	require.Equal(t, "sensitive", aResult["pii"])

	// A second call for profile B after profile A must still return B's values.
	bResult2 := l.Max(profileBLabels, map[string]string{})
	require.Equal(t, "public", bResult2["confidentiality"],
		"repeated profile B call must not be contaminated by prior profile A call")
}
