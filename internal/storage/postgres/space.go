package postgres

import "fmt"

// ActiveSemanticSpaceGenerationSQL returns the shared row predicate used by
// graph, trace, lifecycle, and search reads. The caller supplies only a
// trusted SQL alias from its fixed query; values remain bound parameters.
func ActiveSemanticSpaceGenerationSQL(alias string) string {
	return fmt.Sprintf(`%s.space_generation = dense_mem_active_space_generation(%s.team_id, %s.space_id)`, alias, alias, alias)
}
