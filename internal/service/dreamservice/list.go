package dreamservice

import (
	"errors"
)

const (
	defaultListLimit = 20
	maxListLimit     = 100

	DreamSortUpdatedAt       = "updated_at"
	DreamSortCreatedAt       = "created_at"
	DreamSortLastEvaluatedAt = "last_evaluated_at"
	DreamDirectionAsc        = "asc"
	DreamDirectionDesc       = "desc"
)

var ErrInvalidDreamCursor = errors.New("invalid cursor")
