package evalharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveV2SeedRejectsInvalidOptionsAndSourceState(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	base := DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	}
	for name, mutate := range map[string]func(*DeriveV2SeedOptions){
		"manifest": func(opts *DeriveV2SeedOptions) { opts.SourceManifestPath = "" },
		"suite":    func(opts *DeriveV2SeedOptions) { opts.SourceSuitePath = "" },
		"ledger":   func(opts *DeriveV2SeedOptions) { opts.RelationshipLedgerPath = "" },
		"output":   func(opts *DeriveV2SeedOptions) { opts.OutputDir = "" },
		"seed":     func(opts *DeriveV2SeedOptions) { opts.SeedID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			opts := base
			mutate(&opts)
			if _, err := DeriveV2Seed(opts); err == nil || !strings.Contains(err.Error(), "required") {
				t.Fatalf("DeriveV2Seed(%s) error = %v", name, err)
			}
		})
	}

	manifest, err := LoadSeedManifest(sourceManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = SeedSchemaVersionV2
	if err := writeJSONFile(sourceManifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveV2Seed(base); err == nil || !strings.Contains(err.Error(), "source seed schema_version") {
		t.Fatalf("source schema mismatch error = %v", err)
	}

	// Restore a valid source and make the output path exist to exercise the
	// idempotency guard before any source files are read.
	manifest.SchemaVersion = SeedSchemaVersion
	if err := writeJSONFile(sourceManifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveV2Seed(base); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error = %v", err)
	}
}

func TestDeriveV2SeedRejectsMissingCopiedArtifact(t *testing.T) {
	sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	manifest, err := LoadSeedManifest(sourceManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.QrelsFile = manifest.CasesFile
	if err := writeJSONFile(sourceManifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(sourceManifestPath), "qrels.jsonl")); err != nil {
		t.Fatal(err)
	}
	_, err = DeriveV2Seed(DeriveV2SeedOptions{
		SourceManifestPath:     sourceManifestPath,
		SourceSuitePath:        sourceSuitePath,
		RelationshipLedgerPath: ledgerPath,
		OutputDir:              outputDir,
		SeedID:                 "fixture_v2",
	})
	if err == nil || !strings.Contains(err.Error(), "copy seed artifact cases.jsonl") {
		t.Fatalf("missing artifact error = %v", err)
	}
}

func TestValidateV2DerivationRejectsLineageAndArtifactDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, sourceManifestPath, targetManifestPath, targetCorpusPath, targetSuitePath string)
		want   string
	}{
		{
			name: "source schema",
			mutate: func(t *testing.T, sourcePath, _, _, _ string) {
				manifest, err := LoadSeedManifest(sourcePath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.SchemaVersion = SeedSchemaVersionV2
				if err := writeJSONFile(sourcePath, manifest); err != nil {
					t.Fatal(err)
				}
			},
			want: "source seed schema_version",
		},
		{
			name: "derived schema",
			mutate: func(t *testing.T, _, targetPath, _, _ string) {
				manifest, err := LoadSeedManifest(targetPath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.SchemaVersion = SeedSchemaVersion
				if err := writeJSONFile(targetPath, manifest); err != nil {
					t.Fatal(err)
				}
			},
			want: "derived seed schema_version",
		},
		{
			name: "parent id",
			mutate: func(t *testing.T, _, targetPath, _, _ string) {
				manifest, err := LoadSeedManifest(targetPath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.ParentSeedID = "wrong-parent"
				if err := writeJSONFile(targetPath, manifest); err != nil {
					t.Fatal(err)
				}
			},
			want: "parent_seed_id",
		},
		{
			name: "parent hash",
			mutate: func(t *testing.T, _, targetPath, _, _ string) {
				manifest, err := LoadSeedManifest(targetPath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.ParentSeedHash = "sha256:wrong"
				if err := writeJSONFile(targetPath, manifest); err != nil {
					t.Fatal(err)
				}
			},
			want: "parent_seed_hash",
		},
		{
			name: "artifact declarations",
			mutate: func(t *testing.T, _, targetPath, _, _ string) {
				manifest, err := LoadSeedManifest(targetPath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.CasesFile = "different-cases.jsonl"
				if err := writeJSONFile(targetPath, manifest); err != nil {
					t.Fatal(err)
				}
				original := filepath.Join(filepath.Dir(targetPath), "cases.jsonl")
				if err := copyFileExact(original, filepath.Join(filepath.Dir(targetPath), manifest.CasesFile)); err != nil {
					t.Fatal(err)
				}
			},
			want: "artifact file declarations",
		},
		{
			name: "suite bytes",
			mutate: func(t *testing.T, _, _, _, suitePath string) {
				if err := os.WriteFile(suitePath, []byte("{\"case_id\":\"different\"}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "suite.jsonl is not byte-identical",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
			if _, err := DeriveV2Seed(DeriveV2SeedOptions{
				SourceManifestPath: sourceManifestPath, SourceSuitePath: sourceSuitePath,
				RelationshipLedgerPath: ledgerPath, OutputDir: outputDir, SeedID: "fixture_v2",
			}); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, sourceManifestPath, filepath.Join(outputDir, "seed_manifest.json"), filepath.Join(outputDir, "corpus.jsonl"), filepath.Join(outputDir, derivedSuiteFileName))
			_, err := ValidateV2Derivation(sourceManifestPath, sourceSuitePath, filepath.Join(outputDir, "seed_manifest.json"), filepath.Join(outputDir, derivedSuiteFileName))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateV2Derivation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateV2DerivationRejectsRelationshipAndCountDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, manifestPath, corpusPath string)
		want   string
	}{
		{
			name: "counts",
			mutate: func(t *testing.T, manifestPath, _ string) {
				manifest, err := LoadSeedManifest(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.Counts["corpus"]++
				if err := writeJSONFile(manifestPath, manifest); err != nil {
					t.Fatal(err)
				}
			},
			want: "manifest counts",
		},
		{
			name: "relationship count",
			mutate: func(t *testing.T, manifestPath, corpusPath string) {
				manifest, err := LoadSeedManifest(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.RelationshipCount++
				if err := writeJSONFile(manifestPath, manifest); err != nil {
					t.Fatal(err)
				}
				refreshValidationReport(t, manifestPath, manifest)
			},
			want: "relationship_count",
		},
		{
			name: "relationship contract",
			mutate: func(t *testing.T, manifestPath, corpusPath string) {
				corpus, err := readCorpusFileForTest(corpusPath)
				if err != nil {
					t.Fatal(err)
				}
				corpus[0].Relationships = []any{map[string]any{"bad": true}}
				if err := writeJSONL(corpusPath, corpus); err != nil {
					t.Fatal(err)
				}
				manifest, err := LoadSeedManifest(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				refreshValidationReport(t, manifestPath, manifest)
			},
			want: "relationship contract",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceManifestPath, sourceSuitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
			if _, err := DeriveV2Seed(DeriveV2SeedOptions{
				SourceManifestPath: sourceManifestPath, SourceSuitePath: sourceSuitePath,
				RelationshipLedgerPath: ledgerPath, OutputDir: outputDir, SeedID: "fixture_v2",
			}); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, filepath.Join(outputDir, "seed_manifest.json"), filepath.Join(outputDir, "corpus.jsonl"))
			_, err := ValidateV2Derivation(sourceManifestPath, sourceSuitePath, filepath.Join(outputDir, "seed_manifest.json"), filepath.Join(outputDir, derivedSuiteFileName))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateV2Derivation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func readCorpusFileForTest(path string) ([]CorpusItem, error) {
	var corpus []CorpusItem
	if err := readJSONL(path, &corpus); err != nil {
		return nil, err
	}
	return corpus, nil
}

func TestV2SeedIdentityHashReportsUnsupportedMetadata(t *testing.T) {
	if _, err := corpusEvidenceIdentityHash([]CorpusItem{{SourceDocID: "doc", Content: "content", Metadata: map[string]any{"unsupported": make(chan int)}}}); err == nil {
		t.Fatal("unsupported metadata was hashable")
	}
	if err := validateSameManifestCounts(&SeedManifest{Counts: map[string]int{"corpus": 1}}, &SeedManifest{Counts: map[string]int{"corpus": 2}}); err == nil {
		t.Fatal("different manifest counts unexpectedly matched")
	}
}

func TestV2SeedDirectValidationAndCopyErrorBranches(t *testing.T) {
	root := t.TempDir()
	manifestPath, suitePath, ledgerPath, outputDir := writeV2DerivationFixture(t, "Alpha uses Beta.")
	base := DeriveV2SeedOptions{SourceManifestPath: manifestPath, SourceSuitePath: suitePath, RelationshipLedgerPath: ledgerPath, OutputDir: outputDir, SeedID: "fixture_v2"}

	manifest, err := LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.LicensesFile = "missing-license.md"
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveV2Seed(base); err == nil || !strings.Contains(err.Error(), "hash source seed") {
		t.Fatalf("source hash error = %v", err)
	}

	manifest.LicensesFile = "licenses.md"
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(manifestPath), manifest.CorpusFile), []byte("{bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveV2Seed(base); err == nil {
		t.Fatal("invalid corpus JSON was accepted")
	}

	badOutputParent := filepath.Join(root, "output-parent")
	if err := os.WriteFile(badOutputParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Restore the corpus so the output-parent branch is reached after source
	// loading and ledger validation.
	if err := writeJSONL(filepath.Join(filepath.Dir(manifestPath), manifest.CorpusFile), []CorpusItem{{SourceDocID: "doc-1", Content: "Alpha uses Beta.", Metadata: map[string]any{"axis": "fixture"}}}); err != nil {
		t.Fatal(err)
	}
	badOutput := filepath.Join(badOutputParent, "derived")
	base.OutputDir = badOutput
	if _, err := DeriveV2Seed(base); err == nil {
		t.Fatal("output under a regular file was accepted")
	}

	if _, err := ValidateV2Derivation(manifestPath, suitePath, filepath.Join(root, "missing-manifest.json"), suitePath); err == nil {
		t.Fatal("missing derived manifest was accepted")
	}
	if _, err := ValidateV2Derivation(manifestPath, suitePath, manifestPath, suitePath); err == nil || !strings.Contains(err.Error(), "derived seed schema_version") {
		t.Fatal("V1 manifest was accepted as derived V2")
	}

	// An invalid JSON type is rejected after the ledger decoder has checked the
	// field allowlist.
	badLedger := filepath.Join(root, "bad-ledger.jsonl")
	if err := os.WriteFile(badLedger, []byte(`{"source_doc_id":1,"support":"Alpha uses Beta.","subject":"Alpha","predicate":"uses","object":"Beta"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadRelationshipLedger(badLedger, manifest, "hash", []CorpusItem{{SourceDocID: "doc-1"}}, ""); err == nil {
		t.Fatal("typed ledger field was accepted")
	}

	row := relationshipLedgerRow{Support: "Alpha uses Beta.", Subject: "Alpha", Predicate: "uses", Object: "Beta"}
	for name, mutate := range map[string]func(*relationshipLedgerRow){
		"support":   func(r *relationshipLedgerRow) { r.Support = "missing" },
		"subject":   func(r *relationshipLedgerRow) { r.Subject = "missing" },
		"predicate": func(r *relationshipLedgerRow) { r.Predicate = "missing" },
		"object":    func(r *relationshipLedgerRow) { r.Object = "missing" },
	} {
		t.Run("ledger surface "+name, func(t *testing.T) {
			copyRow := row
			mutate(&copyRow)
			if _, err := flatRelationshipFromLedger(row.Support, copyRow); err == nil {
				t.Fatal("invalid ledger surface was accepted")
			}
		})
	}
	longPredicate := strings.Repeat("x", 129)
	if _, err := flatRelationshipFromLedger(longPredicate, relationshipLedgerRow{Support: longPredicate, Subject: longPredicate, Predicate: longPredicate, Object: longPredicate}); err == nil {
		t.Fatal("overlong ledger predicate was accepted")
	}
	if _, err := flatRelationshipFallback("is uses Beta", relationshipLedgerRow{Predicate: "uses"}); err == nil {
		t.Fatal("stopword subject fallback was accepted")
	}
	if _, err := flatRelationshipFallback("Alpha uses is", relationshipLedgerRow{Predicate: "uses"}); err == nil {
		t.Fatal("stopword object fallback was accepted")
	}
	if _, _, err := fallbackPredicateSpan([]rune("Alpha uses Beta"), "", nil); err == nil {
		t.Fatal("empty fallback predicate was accepted")
	}
	if _, _, ok := nearestFallbackEntity([]rune("Alpha is"), 5, 8, false); ok {
		t.Fatal("stopword-only object range was accepted")
	}
	if sameRunes([]rune("a"), []rune("b")) {
		t.Fatal("different equal-length runes matched")
	}
	if start, end := sentenceSupportSpan([]rune("  Alpha uses Beta.  "), 8, 12); string([]rune("  Alpha uses Beta.  ")[start:end]) != "Alpha uses Beta." {
		t.Fatalf("trimmed sentence span = %q", string([]rune("  Alpha uses Beta.  ")[start:end]))
	}

	copySource := filepath.Join(root, "copy-source")
	if err := os.WriteFile(copySource, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copySeedArtifacts(manifestPath, &SeedManifest{CasesFile: "../escape"}, filepath.Join(root, "copy")); err == nil {
		t.Fatal("unsafe artifact copy was accepted")
	}
	if _, err := validateCopiedSeedArtifacts(manifestPath, &SeedManifest{CasesFile: "../escape"}, manifestPath, &SeedManifest{CasesFile: "../escape"}); err == nil {
		t.Fatal("unsafe artifact validation was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "copy-target"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFileExact(copySource, filepath.Join(root, "copy-target", "child")); err == nil {
		t.Fatal("copy under a regular file was accepted")
	}
	if _, err := validateCopiedFile(filepath.Join(root, "missing-source"), copySource, "fixture"); err == nil {
		t.Fatal("missing source validation was accepted")
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := sha256File(directory); err == nil {
		t.Fatal("directory hash was accepted")
	}
	if _, err := sameFileBytes(copySource, filepath.Join(root, "missing-right")); err == nil {
		t.Fatal("missing comparison file was accepted")
	}
}

func refreshValidationReport(t *testing.T, manifestPath string, manifest *SeedManifest) {
	t.Helper()
	hash, err := SeedHash(manifestPath, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(filepath.Dir(manifestPath), manifest.ValidationReportFile), seedValidationReport{
		SchemaVersion: "dense-mem.eval.validation.v1",
		SeedID:        manifest.SeedID,
		Status:        "passed",
		SeedHash:      hash,
	}); err != nil {
		t.Fatal(err)
	}
}
