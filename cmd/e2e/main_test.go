package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGroupCasesCapsPackageBatches(t *testing.T) {
	cases := make([]databaseCase, 0, maxDatabaseCasesPerBatch+1)
	for index := 0; index < maxDatabaseCasesPerBatch+1; index++ {
		cases = append(cases, databaseCase{ID: fmt.Sprintf("case-%03d", index), Package: "./internal/repository"})
	}
	cases = append(cases, databaseCase{ID: "other", Package: "./internal/http"})

	batches := groupCases(cases)
	if len(batches) != 3 {
		t.Fatalf("expected three batches, got %d", len(batches))
	}
	if batches[0].Package != "./internal/http" || len(batches[0].Cases) != 1 {
		t.Fatalf("unexpected first batch: %+v", batches[0])
	}
	if batches[1].Package != "./internal/repository" || len(batches[1].Cases) != maxDatabaseCasesPerBatch {
		t.Fatalf("unexpected first repository batch: %+v", batches[1])
	}
	if batches[2].Package != "./internal/repository" || len(batches[2].Cases) != 1 {
		t.Fatalf("unexpected second repository batch: %+v", batches[2])
	}
}

func TestRunBatchRequiresEveryCaseToPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake go command uses a POSIX shell")
	}

	tests := []struct {
		name       string
		output     string
		exitStatus int
		want       string
	}{
		{name: "pass", output: `{"Action":"pass","Test":"TestRequired"}`, want: ""},
		{name: "skip", output: `{"Action":"skip","Test":"TestRequired"}`, want: "ended with skip"},
		{name: "missing", output: `{"Action":"pass","Test":"TestOther"}`, want: "did not execute"},
		{name: "command failure", output: `{"Action":"fail","Test":"TestRequired"}`, exitStatus: 1, want: "batch ./internal/repository failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			fakeGo := filepath.Join(binDir, "go")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\nexit %d\n", tc.output, tc.exitStatus)
			if err := os.WriteFile(fakeGo, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			err := runBatch(t.TempDir(), filepath.Join(t.TempDir(), "overlay.json"), packageBatch{
				Package: "./internal/repository",
				Cases:   []databaseCase{{ID: "required", Run: "^TestRequired$"}},
			}, 10*time.Second)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("runBatch() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runBatch() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
