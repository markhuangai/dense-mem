package postgres

import (
	"strconv"
	"strings"
)

func evaluationCursorOffset(cursor string) int {
	offset, err := strconv.Atoi(strings.TrimSpace(cursor))
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}
