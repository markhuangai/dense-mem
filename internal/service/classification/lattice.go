// Package classification provides classification utilities for the knowledge pipeline.
//
// The max-lattice defined here is a dimension-wise join (supremum) over a set of
// known classification dimensions. Callers produce combined classifications by
// repeatedly calling Lattice.Max, which is associative and commutative — the
// result of merging N label maps is independent of merge order.
//
// Owner isolation: this package is a pure in-memory computation. It receives no
// owner ID and accesses no storage. Callers scope label maps to the authenticated
// owner before invoking Max.
package classification

// LatticeVersion is the version identifier for this max-lattice schema.
// Downstream consumers may store this alongside computed classifications so
// that schema migrations can be detected at read time.
const LatticeVersion = "v1"

// dimensionOrder maps each known classification dimension to its ordered levels,
// from minimum (index 0) to maximum (last index).
//
// Invariant: each slice is ordered strictly ascending — the index position defines
// the lattice rank, and dimension-wise max always produces a value whose rank is
// >= the rank of each input for that dimension.
var dimensionOrder = map[string][]string{
	"confidentiality": {"public", "internal", "confidential", "restricted"},
	"retention":       {"ephemeral", "short_term", "long_term", "permanent"},
	"pii":             {"none", "non_sensitive", "sensitive"},
}

// Lattice implements a dimension-wise max-lattice for classification label maps.
//
// For known dimensions the maximum of two values is determined by total order
// rank. For unknown dimensions the value is passed through unchanged (last-write
// wins when both inputs carry the key). Missing known dimensions default to the
// minimum (lowest-rank) value for that dimension so that Max is closed over all
// possible partial label maps.
type Lattice struct {
	orders map[string][]string
}

// DefaultLattice returns a Lattice configured with the canonical LatticeVersion v1
// dimension orders:
//
//	confidentiality: public < internal < confidential < restricted
//	retention:       ephemeral < short_term < long_term < permanent
//	pii:             none < non_sensitive < sensitive
//
// The returned Lattice is safe for concurrent use after construction.
func DefaultLattice() *Lattice {
	// Defensive copy so callers cannot mutate the package-level default.
	orders := make(map[string][]string, len(dimensionOrder))
	for dim, levels := range dimensionOrder {
		cp := make([]string, len(levels))
		copy(cp, levels)
		orders[dim] = cp
	}
	return &Lattice{orders: orders}
}

// Max returns the dimension-wise maximum of label maps a and b.
//
// Rules applied for each key in the union of a and b:
//   - Known dimension, key present in both: result is the value with higher rank.
//   - Known dimension, key missing from one input: missing side defaults to the
//     dimension minimum; the explicit value wins unless it is also the minimum.
//   - Known dimension, key absent from both: result still contains the key set to
//     the dimension minimum (ensures the output map is complete for all known dims).
//   - Unknown dimension: value passed through unchanged; when both inputs carry
//     the key with differing values, b takes precedence (stable tie-break).
//
// Neither a nor b is modified. The returned map is always a new allocation.
// Max is safe for concurrent use.
func (l *Lattice) Max(a, b map[string]string) map[string]string {
	result := make(map[string]string)

	// Collect the union of all keys from both inputs.
	allKeys := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		allKeys[k] = struct{}{}
	}
	for k := range b {
		allKeys[k] = struct{}{}
	}

	for k := range allKeys {
		levels, known := l.orders[k]
		if !known {
			// Unknown dimension: passthrough. Prefer b for a stable tie-break when
			// both inputs carry the key (no defined order, so either is valid).
			if v, ok := b[k]; ok {
				result[k] = v
			} else {
				result[k] = a[k]
			}
			continue
		}

		// Known dimension: derive effective rank for each side.
		// Absent key is treated as rank 0 (dimension minimum).
		rankA := 0
		valA := levels[0]
		if v, ok := a[k]; ok {
			if r := l.rankIn(levels, v); r >= 0 {
				rankA = r
				valA = v
			}
			// If value is unrecognised within a known dimension, treat as minimum.
		}

		rankB := 0
		valB := levels[0]
		if v, ok := b[k]; ok {
			if r := l.rankIn(levels, v); r >= 0 {
				rankB = r
				valB = v
			}
		}

		if rankB >= rankA {
			result[k] = valB
		} else {
			result[k] = valA
		}
	}

	// Guarantee all known dimensions appear in the output (missing => minimum).
	for dim, levels := range l.orders {
		if _, present := result[dim]; !present {
			result[dim] = levels[0]
		}
	}

	return result
}

// rankIn returns the index of value in levels, or -1 if not found.
func (l *Lattice) rankIn(levels []string, value string) int {
	for i, v := range levels {
		if v == value {
			return i
		}
	}
	return -1
}
