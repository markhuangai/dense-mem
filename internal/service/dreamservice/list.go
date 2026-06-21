package dreamservice

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
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

type dreamCursor struct {
	Sort      string
	Direction string
	SortAt    time.Time
	DreamID   string
}

func normalizeDreamSort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case DreamSortCreatedAt:
		return DreamSortCreatedAt
	case DreamSortLastEvaluatedAt:
		return DreamSortLastEvaluatedAt
	default:
		return DreamSortUpdatedAt
	}
}

func normalizeDreamDirection(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == DreamDirectionAsc {
		return DreamDirectionAsc
	}
	return DreamDirectionDesc
}

func encodeDreamCursor(c dreamCursor) string {
	raw := fmt.Sprintf("%s|%s|%s|%s", c.Sort, c.Direction, c.SortAt.UTC().Format(time.RFC3339Nano), c.DreamID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeDreamCursor(cursor string, sort string, direction string) (time.Time, string, error) {
	if strings.TrimSpace(cursor) == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrInvalidDreamCursor, err)
	}
	parts := strings.SplitN(string(raw), "|", 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return time.Time{}, "", ErrInvalidDreamCursor
	}
	if parts[0] != sort || parts[1] != direction {
		return time.Time{}, "", fmt.Errorf("%w: sort changed", ErrInvalidDreamCursor)
	}
	sortAt, err := time.Parse(time.RFC3339Nano, parts[2])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrInvalidDreamCursor, err)
	}
	return sortAt.UTC(), parts[3], nil
}

func dreamSortExpression(sort string) string {
	switch sort {
	case DreamSortCreatedAt:
		return "d.created_at"
	case DreamSortLastEvaluatedAt:
		return "coalesce(d.last_evaluated_at, datetime({epochMillis: 0}))"
	default:
		return "d.updated_at"
	}
}

func dreamSortTime(dream *domain.Dream, sort string) time.Time {
	if dream == nil {
		return time.Time{}
	}
	switch sort {
	case DreamSortCreatedAt:
		return dream.CreatedAt
	case DreamSortLastEvaluatedAt:
		if dream.LastEvaluatedAt != nil {
			return *dream.LastEvaluatedAt
		}
		return time.Unix(0, 0).UTC()
	default:
		return dream.UpdatedAt
	}
}
