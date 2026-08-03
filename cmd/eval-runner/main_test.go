package main

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func TestImportConcurrencyDefault(t *testing.T) {
	t.Setenv("DENSE_MEM_EVAL_IMPORT_CONCURRENCY", "")
	if got := importConcurrencyDefault(); got != evalharness.DefaultImportConcurrency {
		t.Fatalf("import concurrency default = %d, want %d", got, evalharness.DefaultImportConcurrency)
	}
}

func TestImportConcurrencyDefaultUsesEnvironment(t *testing.T) {
	t.Setenv("DENSE_MEM_EVAL_IMPORT_CONCURRENCY", "7")
	if got := importConcurrencyDefault(); got != 7 {
		t.Fatalf("import concurrency default = %d, want 7", got)
	}
}
