package evalharness

import (
	"encoding/json"
	"fmt"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"sort"
	"strings"
	"time"
)

type PairScope struct {
	TeamID     string `json:"team_id"`
	OwnerID    string `json:"owner_id"`
	SpaceID    string `json:"space_id"`
	Generation int64  `json:"generation"`
}

type PairCapacity struct {
	Global              int   `json:"global"`
	Team                int   `json:"team"`
	ContextWindowTokens int64 `json:"context_window_tokens"`
}

type PairImportAttempt struct {
	Scope         PairScope `json:"scope"`
	SourceDocID   string    `json:"source_doc_id"`
	AttemptID     string    `json:"attempt_id"`
	RequestHash   string    `json:"request_hash"`
	Outcome       string    `json:"outcome"`
	EvidenceIDs   []string  `json:"evidence_ids"`
	ContentHashes []string  `json:"content_hashes"`
	Live          bool      `json:"live"`
}

type PairImportProgress struct {
	Complete           bool     `json:"complete"`
	CompletedSourceIDs []string `json:"completed_source_ids"`
	PendingSourceIDs   []string `json:"pending_source_ids"`
}

func EvaluatePairImport(scope PairScope, corpus []CorpusItem, attempts []PairImportAttempt, fresh bool) (PairImportProgress, error) {
	result := PairImportProgress{CompletedSourceIDs: []string{}, PendingSourceIDs: []string{}}
	if fresh && len(attempts) != 0 {
		return result, fmt.Errorf("paired import requires a fresh runtime")
	}
	wanted := sourceDocIDIndexForCorpus(corpus)
	contents := map[string]string{}
	requests := map[string]string{}
	for _, item := range corpus {
		hash, err := pairRequestHash(item)
		if err != nil {
			return result, err
		}
		requests[item.SourceDocID] = hash
		contents[item.SourceDocID] = strings.TrimPrefix(pairBytesHash([]byte(item.Content)), "sha256:")
	}
	seen, identities := map[string]bool{}, map[string]bool{}
	for _, row := range attempts {
		if row.Scope != scope {
			return result, fmt.Errorf("import receipt has a different team, owner, space or generation")
		}
		if _, ok := wanted[row.SourceDocID]; !ok {
			return result, fmt.Errorf("import receipt references unknown source %q", row.SourceDocID)
		}
		if row.AttemptID == "" || row.RequestHash != requests[row.SourceDocID] {
			return result, fmt.Errorf("import receipt does not match its exact request or attempt identity")
		}
		if seen[row.SourceDocID] || identities[row.AttemptID] {
			return result, fmt.Errorf("duplicate import receipt for source %q", row.SourceDocID)
		}
		if row.Outcome != "completed" || !row.Live || len(row.EvidenceIDs) == 0 {
			return result, fmt.Errorf("source %q has a failed, unfinished or stale import receipt; automatic retry is forbidden", row.SourceDocID)
		}
		if len(row.ContentHashes) != len(row.EvidenceIDs) {
			return result, fmt.Errorf("source %q lacks exact evidence content hashes", row.SourceDocID)
		}
		for _, hash := range row.ContentHashes {
			if strings.TrimPrefix(hash, "sha256:") != contents[row.SourceDocID] {
				return result, fmt.Errorf("source %q evidence content differs from the frozen input", row.SourceDocID)
			}
		}
		seen[row.SourceDocID], identities[row.AttemptID] = true, true
	}
	for _, item := range corpus {
		if seen[item.SourceDocID] {
			result.CompletedSourceIDs = append(result.CompletedSourceIDs, item.SourceDocID)
		} else {
			result.PendingSourceIDs = append(result.PendingSourceIDs, item.SourceDocID)
		}
	}
	result.Complete = len(result.PendingSourceIDs) == 0
	return result, nil
}

func pairRequestHash(item CorpusItem) (string, error) {
	input := corpusRememberInput(item)
	input["relationship_hints"] = input["relationships"]
	delete(input, "relationships")
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	var request remember.RememberRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return "", err
	}
	return remember.CanonicalRequestHash(request)
}

type PairUsage struct {
	Attempts     int64 `json:"attempts"`
	Unreconciled int64 `json:"unreconciled"`
	Unavailable  int64 `json:"unavailable"`
	Partial      int64 `json:"partial"`
	Input        int64 `json:"reported_input"`
	Output       int64 `json:"reported_output"`
	Total        int64 `json:"reported_total"`
}

type PairObservation struct {
	ObservedAt                 time.Time             `json:"observed_at"`
	Scope                      PairScope             `json:"scope"`
	Enabled                    bool                  `json:"enabled"`
	Paused                     bool                  `json:"paused"`
	DiscoveryComplete          bool                  `json:"discovery_complete"`
	CoverageComplete           bool                  `json:"coverage_complete"`
	Eligible                   int64                 `json:"eligible"`
	Organized                  int64                 `json:"organized"`
	Pending                    int64                 `json:"pending"`
	Failed                     int64                 `json:"failed"`
	Ambiguous                  int64                 `json:"ambiguous"`
	ActiveWork                 int64                 `json:"active_work"`
	ActiveBatches              int64                 `json:"active_batches"`
	ActivePreparations         int64                 `json:"active_preparations"`
	ActiveProjections          int64                 `json:"active_projections"`
	PendingProjections         int64                 `json:"pending_projections"`
	FailedProjections          int64                 `json:"failed_projections"`
	ProjectionCoverageComplete bool                  `json:"projection_coverage_complete"`
	SearchConverged            bool                  `json:"search_converged"`
	Usage                      PairUsage             `json:"usage"`
	HistoricalUsage            PairUsage             `json:"historical_usage"`
	Policy                     PairCapacity          `json:"policy"`
	Model                      string                `json:"model"`
	WindowID                   string                `json:"window_id"`
	ReservedInput              int64                 `json:"reserved_input"`
	ReservedOutput             int64                 `json:"reserved_output"`
	BudgetDeferred             int64                 `json:"budget_deferred"`
	Overrun                    bool                  `json:"overrun"`
	CanonicalHash              string                `json:"canonical_hash"`
	SourceHash                 string                `json:"source_hash"`
	Imports                    []PairImportAttempt   `json:"imports"`
	ProviderAttempts           []PairProviderAttempt `json:"provider_attempts"`
}

type PairReadiness struct {
	Ready             bool     `json:"ready"`
	Complete          bool     `json:"complete"`
	WaitingFor        []string `json:"waiting_for"`
	IncompleteReasons []string `json:"incomplete_reasons"`
}

func EvaluatePairReadiness(protocol string, observation PairObservation, frozen bool) (PairReadiness, error) {
	r := PairReadiness{WaitingFor: []string{}, IncompleteReasons: []string{}}
	if protocol != "adr0014" && protocol != "legacy" && protocol != "none" {
		return r, fmt.Errorf("unsupported paired readiness protocol %q", protocol)
	}
	if observation.ObservedAt.IsZero() || observation.SourceHash == "" || observation.CanonicalHash == "" {
		return r, fmt.Errorf("readiness requires observed time and source/canonical fingerprints")
	}
	if !observation.SearchConverged {
		r.WaitingFor = append(r.WaitingFor, "search")
	}
	if protocol != "none" {
		if !observation.Enabled {
			return r, fmt.Errorf("ontology must remain enabled")
		}
		if observation.WindowID == "" {
			r.WaitingFor = append(r.WaitingFor, "active_window")
		}
		if !observation.DiscoveryComplete {
			r.WaitingFor = append(r.WaitingFor, "discovery")
		}
		for _, count := range []struct {
			name string
			n    int64
		}{
			{"pending_sources", observation.Pending}, {"active_work", observation.ActiveWork},
			{"active_batches", observation.ActiveBatches}, {"preparations", observation.ActivePreparations},
			{"active_projections", observation.ActiveProjections}, {"pending_projections", observation.PendingProjections},
			{"unreconciled_attempts", observation.Usage.Unreconciled},
		} {
			if count.n < 0 {
				return r, fmt.Errorf("negative readiness count %s", count.name)
			}
			if count.n > 0 {
				r.WaitingFor = append(r.WaitingFor, count.name)
			}
		}
		if frozen && !observation.Paused {
			r.WaitingFor = append(r.WaitingFor, "pause")
		}
		if protocol == "legacy" && (observation.ReservedInput != 0 || observation.ReservedOutput != 0 || observation.BudgetDeferred != 0 || observation.Overrun) {
			r.WaitingFor = append(r.WaitingFor, "recorded_legacy_accounting_gate")
		}
		if !observation.CoverageComplete || observation.Failed > 0 || observation.Ambiguous > 0 {
			r.IncompleteReasons = append(r.IncompleteReasons, "ontology_coverage")
		}
		if !observation.ProjectionCoverageComplete || observation.FailedProjections > 0 {
			r.IncompleteReasons = append(r.IncompleteReasons, "projection_coverage")
		}
	}
	r.Ready = len(r.WaitingFor) == 0
	r.Complete = r.Ready && len(r.IncompleteReasons) == 0
	return r, nil
}

type PairProviderAttempt struct {
	TeamID         string    `json:"team_id"`
	SpaceID        string    `json:"space_id"`
	Generation     int64     `json:"generation"`
	WindowID       string    `json:"window_id"`
	OperationKey   string    `json:"operation_key"`
	AssessmentID   string    `json:"assessment_id"`
	Number         int       `json:"number"`
	AdmittedAt     time.Time `json:"admitted_at"`
	ReconciledAt   time.Time `json:"reconciled_at"`
	Terminal       bool      `json:"terminal"`
	UsageAvailable bool      `json:"usage_available"`
	InputTokens    int64     `json:"input_tokens"`
	OutputTokens   int64     `json:"output_tokens"`
	TotalTokens    int64     `json:"total_tokens"`
	Outcome        string    `json:"outcome"`
}

type PairAttemptMeasurement struct {
	Attempts         int            `json:"attempts"`
	Unfinished       int            `json:"unfinished"`
	KnownUsage       int            `json:"known_usage"`
	UnknownUsage     int            `json:"unknown_usage"`
	PartialUsage     int            `json:"partial_usage"`
	ReportedInput    int64          `json:"reported_input"`
	ReportedOutput   int64          `json:"reported_output"`
	ReportedTotal    int64          `json:"reported_total"`
	Outcomes         map[string]int `json:"outcomes"`
	IntervalStatus   string         `json:"interval_status"`
	InvalidIntervals int            `json:"invalid_intervals"`
	MaximumOverlap   int            `json:"maximum_overlap"`
	CoveredSeconds   float64        `json:"covered_seconds"`
	AttemptSeconds   float64        `json:"attempt_seconds"`
	SpanSeconds      float64        `json:"span_seconds"`
}

func MeasurePairAttempts(attempts []PairProviderAttempt) (PairAttemptMeasurement, error) {
	r := PairAttemptMeasurement{Attempts: len(attempts), IntervalStatus: "available", Outcomes: map[string]int{}}
	seen := map[string]bool{}
	type event struct {
		at    time.Time
		delta int
	}
	events := []event{}
	for _, a := range attempts {
		key := fmt.Sprintf("%s/%s/%d/%s/%s/%d", a.TeamID, a.SpaceID, a.Generation, a.OperationKey, a.AssessmentID, a.Number)
		if a.TeamID == "" || a.SpaceID == "" || a.Generation < 1 || a.OperationKey == "" || a.AssessmentID == "" || a.Number < 1 || seen[key] {
			return r, fmt.Errorf("missing or duplicate full assessment receipt identity")
		}
		if a.InputTokens < 0 || a.OutputTokens < 0 || a.TotalTokens < 0 || a.UsageAvailable != (a.InputTokens > 0 || a.OutputTokens > 0 || a.TotalTokens > 0) {
			return r, fmt.Errorf("invalid provider usage receipt")
		}
		seen[key] = true
		r.Outcomes[a.Outcome]++
		r.ReportedInput += a.InputTokens
		r.ReportedOutput += a.OutputTokens
		r.ReportedTotal += a.TotalTokens
		if !a.Terminal {
			r.Unfinished++
			continue
		}
		if !a.UsageAvailable {
			r.UnknownUsage++
		} else if a.InputTokens == 0 || a.OutputTokens == 0 {
			r.PartialUsage++
		} else {
			r.KnownUsage++
		}
		if a.AdmittedAt.IsZero() || a.ReconciledAt.IsZero() || a.ReconciledAt.Before(a.AdmittedAt) {
			r.InvalidIntervals++
			continue
		}
		events = append(events, event{a.AdmittedAt, 1}, event{a.ReconciledAt, -1})
		r.AttemptSeconds += a.ReconciledAt.Sub(a.AdmittedAt).Seconds()
	}
	if r.InvalidIntervals > 0 || r.Unfinished > 0 {
		r.IntervalStatus, r.AttemptSeconds = "unavailable", 0
		return r, nil
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	active := 0
	for i := 0; i < len(events); {
		at, delta := events[i].at, 0
		if i > 0 && active > 0 {
			r.CoveredSeconds += at.Sub(events[i-1].at).Seconds()
		}
		for i < len(events) && events[i].at.Equal(at) {
			delta += events[i].delta
			i++
		}
		active += delta
		r.MaximumOverlap = max(r.MaximumOverlap, active)
	}
	if len(events) > 0 {
		r.SpanSeconds = events[len(events)-1].at.Sub(events[0].at).Seconds()
	}
	return r, nil
}
