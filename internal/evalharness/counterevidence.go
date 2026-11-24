package evalharness

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

type CounterevidenceMeasurement struct {
	ExpectedClaims  int     `json:"expected_claims"`
	ScoredClaims    int     `json:"scored_claims"`
	BothSides       int     `json:"both_sides_at_10"`
	Supports        int     `json:"supports_at_10"`
	Refutes         int     `json:"refutes_at_10"`
	ExpectedIntake  int     `json:"expected_intake"`
	CompletedIntake int     `json:"completed_intake"`
	IntakeTimeMS    float64 `json:"intake_time_ms"`
	RecallP95MS     float64 `json:"recall_p95_ms"`
	SourceHash      string  `json:"source_sha256"`
}

type climateSource struct {
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Claims   int    `json:"claims"`
	Disputed int    `json:"disputed"`
}

type climateClaim struct {
	ID       string `json:"claim_id"`
	Text     string `json:"claim"`
	Label    string `json:"claim_label"`
	Evidence []struct {
		ID    string `json:"evidence_id"`
		Text  string `json:"evidence"`
		Label string `json:"evidence_label"`
	} `json:"evidences"`
}

func MeasureCounterevidence(ctx context.Context, registryPath string, client *HTTPClient, native *Native) (measurement CounterevidenceMeasurement, err error) {
	if err := RequireRunner(); err != nil {
		return measurement, err
	}
	var source climateSource
	if err := readJSON(registryPath, &source); err != nil {
		return measurement, err
	}
	measurement.ExpectedClaims, measurement.SourceHash = source.Disputed, source.SHA256
	claims, corpus, err := readCounterevidence(ctx, source)
	if err != nil {
		return measurement, err
	}
	measurement.ExpectedIntake = len(corpus)
	start := time.Now()
	mapping, err := client.importCorpus(ctx, corpus, 4, native)
	measurement.IntakeTimeMS = float64(time.Since(start).Microseconds()) / 1000
	measurement.CompletedIntake = len(mapping.BySourceDocID)
	if err != nil || measurement.CompletedIntake != measurement.ExpectedIntake {
		return measurement, errors.Join(err, errors.New("counterevidence intake incomplete"))
	}

	sourceByEvidence := make(map[string][]string)
	for sourceID, parts := range mapping.Partitions {
		for _, part := range parts {
			if !slices.Contains(sourceByEvidence[part.EvidenceID], sourceID) {
				sourceByEvidence[part.EvidenceID] = append(sourceByEvidence[part.EvidenceID], sourceID)
			}
		}
	}
	for sourceID, ref := range mapping.BySourceDocID {
		if !slices.Contains(sourceByEvidence[ref.ID], sourceID) {
			sourceByEvidence[ref.ID] = append(sourceByEvidence[ref.ID], sourceID)
		}
	}

	latencies := make([]float64, 0, len(claims))
	for _, claim := range claims {
		initial := memoryCall(ctx, client, "recall_memory", map[string]any{"query": claim.Text, "limit": 10, "topic_limit": 0})
		if callErr := validateInitial(initial); callErr != nil {
			return measurement, callErr
		}
		supports, refutes := matchCounterevidence(claim, initial, sourceByEvidence)
		measurement.ScoredClaims++
		if supports {
			measurement.Supports++
		}
		if refutes {
			measurement.Refutes++
		}
		if supports && refutes {
			measurement.BothSides++
		}
		latencies = append(latencies, initial.ElapsedMS)
	}
	sort.Float64s(latencies)
	measurement.RecallP95MS = latencies[(len(latencies)*95+99)/100-1]
	return measurement, nil
}

func matchCounterevidence(claim climateClaim, initial MemoryCall, sourceByEvidence map[string][]string) (supports, refutes bool) {
	results, _ := initial.Result["results"].([]any)
	for _, raw := range results[:min(len(results), 10)] {
		hit, _ := raw.(map[string]any)
		for _, evidence := range claim.Evidence {
			if !slices.Contains(sourceByEvidence[stringValue(hit["evidence_id"])], climateEvidenceKey(evidence.ID)) {
				continue
			}
			quote := stringValue(hit["context"])
			if quote == "" || !strings.Contains(evidence.Text, quote) {
				continue
			}
			supports = supports || evidence.Label == "SUPPORTS"
			refutes = refutes || evidence.Label == "REFUTES"
		}
	}
	return supports, refutes
}

func readCounterevidence(ctx context.Context, source climateSource) ([]climateClaim, []CorpusItem, error) {
	if !strings.HasPrefix(source.URL, "https://raw.githubusercontent.com/tdiggelm/climate-fever-dataset/") || source.Disputed <= 0 {
		return nil, nil, errors.New("invalid locked counterevidence source")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, nil, err
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("counterevidence download HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 5<<20))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != source.SHA256 {
		return nil, nil, errors.Join(err, errors.New("counterevidence source checksum differs"))
	}
	count := 0
	seen := make(map[string]string)
	var claims []climateClaim
	var corpus []CorpusItem
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), 128<<10)
	for scanner.Scan() {
		var claim climateClaim
		if err := json.Unmarshal(scanner.Bytes(), &claim); err != nil {
			return nil, nil, err
		}
		count++
		if claim.Label != "DISPUTED" {
			continue
		}
		var supports, refutes bool
		for _, evidence := range claim.Evidence {
			supports = supports || evidence.Label == "SUPPORTS"
			refutes = refutes || evidence.Label == "REFUTES"
			key := climateEvidenceKey(evidence.ID)
			if existing, ok := seen[key]; ok {
				if existing != evidence.Text {
					return nil, nil, errors.New("counterevidence source changed text")
				}
				continue
			}
			seen[key] = evidence.Text
			corpus = append(corpus, CorpusItem{SourceDocID: key, Content: evidence.Text, SourceDataset: "climate-fever"})
		}
		if !supports || !refutes {
			return nil, nil, errors.New("disputed claim lacks a judged side")
		}
		claims = append(claims, claim)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	if count != source.Claims || len(claims) != source.Disputed {
		return nil, nil, errors.New("counterevidence cohort size differs")
	}
	return claims, corpus, nil
}

func climateEvidenceKey(id string) string {
	return "climate-" + fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
}
