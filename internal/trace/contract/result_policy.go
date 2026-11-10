package contract

func BoundEvents[T any](values []T, remaining *int, result *EvidenceTraceResult, kind string) []T {
	if len(values) > *remaining {
		values = values[:*remaining]
		result.Omissions = append(result.Omissions, kind+"_max_events")
		result.Truncated = true
	}
	*remaining -= len(values)
	return values
}
