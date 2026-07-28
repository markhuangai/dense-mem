package postgres

import (
	"bytes"
	"context"
	"testing"
)

type gormParamsFilter interface {
	ParamsFilter(context.Context, string, ...interface{}) (string, []interface{})
}

func TestGORMLoggerOmitsBoundParameters(t *testing.T) {
	logger := newGORMLogger(&bytes.Buffer{})
	filter, ok := logger.(gormParamsFilter)
	if !ok {
		t.Fatal("GORM logger does not expose parameter filtering")
	}

	const query = "SELECT status FROM teams WHERE id = $1"
	filteredQuery, params := filter.ParamsFilter(context.Background(), query, "team-private-value")
	if filteredQuery != query {
		t.Fatalf("filtered query = %q, want %q", filteredQuery, query)
	}
	if len(params) != 0 {
		t.Fatalf("filtered parameters = %#v, want none", params)
	}
}
