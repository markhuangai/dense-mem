package observability

import (
	"errors"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"
)

var exportMetricNames = strings.Fields(`
densemem_http_requests_total densemem_http_request_duration_seconds
densemem_embedding_requests_total densemem_embedding_errors_total densemem_embedding_duration_seconds densemem_embedding_tokens_total
densemem_verifier_requests_total densemem_verifier_duration_seconds densemem_verifier_tokens_total
densemem_recall_requests_total densemem_recall_duration_seconds densemem_recall_results
densemem_verify_verdict_total densemem_remember_acknowledgements_total densemem_remember_acknowledgement_duration_seconds
densemem_ai_operation_tokens_total densemem_ai_operation_cost_usd_total densemem_ai_operation_items_total densemem_ai_operation_unpriced_total
densemem_assessor_requests_total densemem_assessor_duration_seconds densemem_assessor_tokens_total
densemem_mcp_transport_requests_total densemem_mcp_transport_duration_seconds densemem_mcp_tool_results_total
densemem_logical_operation_attempts_total densemem_logical_operation_duration_seconds densemem_logical_operation_recoveries_total
densemem_remember_phase_duration_seconds densemem_operation_provider_tokens_total densemem_operation_provider_usage_unpriced_total
densemem_read_stage_duration_seconds densemem_read_sql_statements_total densemem_read_stage_items
`)

var exportLabelValues = map[string]string{
	"outcome":        "ok success error failure cancelled rpc_error tool_error missing_result accepted rejected partial stored not_stored completed failed unknown",
	"operation":      "remember recall trace semantic_assessment conflict_review dream_generation recall_embedding search_document_embedding community_summary ontology_organization search",
	"component":      "verifier embedding assessor",
	"kind":           "input output prompt completion total",
	"source":         "provider tokenizer",
	"method":         "GET POST PUT PATCH DELETE HEAD OPTIONS",
	"status_class":   "2xx 3xx 4xx 5xx",
	"classification": "execution replay conflict recovery initial retry",
}

type exportGatherer struct {
	source prometheus.Gatherer
	models map[string]bool
	health *exportHealthState
}

func exportAllowed(value, allowlist string) string {
	for _, allowed := range strings.Fields(allowlist) {
		if value == allowed {
			return value
		}
	}
	return "other"
}

func (g exportGatherer) labels(metric *dto.Metric) ([]*dto.LabelPair, string) {
	labels := make([]*dto.LabelPair, 0)
	for _, label := range metric.GetLabel() {
		name, value := label.GetName(), label.GetValue()
		if name == "model" {
			if !g.models[value] {
				value = "other"
			}
		} else {
			allowlist, ok := exportLabelValues[name]
			if !ok {
				continue
			}
			value = exportAllowed(value, allowlist)
		}
		labels = append(labels, &dto.LabelPair{Name: proto.String(name), Value: proto.String(value)})
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i].GetName() < labels[j].GetName() })
	var key strings.Builder
	for _, label := range labels {
		key.WriteString(label.GetName())
		key.WriteByte(0)
		key.WriteString(label.GetValue())
		key.WriteByte(0)
	}
	return labels, key.String()
}

// Gather merges identity-scoped series before the bridge converts them, and discards exemplars and unapproved labels.
func (g exportGatherer) Gather() (result []*dto.MetricFamily, err error) {
	defer func() {
		if g.health != nil {
			g.health.recordCollection(err)
		}
	}()
	families, err := g.source.Gather()
	if err != nil {
		return nil, errors.New("OTLP metric collection failed")
	}
	allowed := make(map[string]bool, len(exportMetricNames))
	for _, name := range exportMetricNames {
		allowed[name] = true
	}
	result = make([]*dto.MetricFamily, 0)
	for _, family := range families {
		if !allowed[family.GetName()] {
			continue
		}
		kind := family.GetType()
		if kind != dto.MetricType_COUNTER && kind != dto.MetricType_HISTOGRAM {
			return nil, errors.New("OTLP metric type is unsupported")
		}
		groups := make(map[string]*dto.Metric)
		for _, metric := range family.Metric {
			labels, key := g.labels(metric)
			target := groups[key]
			if target == nil {
				target = &dto.Metric{Label: labels}
				groups[key] = target
			}
			if len(groups) > 4096 {
				return nil, errors.New("OTLP metric series bound exceeded")
			}
			if kind == dto.MetricType_COUNTER {
				if target.Counter == nil {
					target.Counter = &dto.Counter{Value: proto.Float64(0)}
				}
				*target.Counter.Value += metric.GetCounter().GetValue()
				continue
			}
			hist := metric.GetHistogram()
			if target.Histogram == nil {
				target.Histogram = &dto.Histogram{SampleCount: proto.Uint64(0), SampleSum: proto.Float64(0)}
				for _, bucket := range hist.GetBucket() {
					target.Histogram.Bucket = append(target.Histogram.Bucket, &dto.Bucket{UpperBound: proto.Float64(bucket.GetUpperBound()), CumulativeCount: proto.Uint64(0)})
				}
			}
			if len(target.Histogram.Bucket) != len(hist.GetBucket()) {
				return nil, errors.New("OTLP histogram bounds differ")
			}
			*target.Histogram.SampleCount += hist.GetSampleCount()
			*target.Histogram.SampleSum += hist.GetSampleSum()
			for i, bucket := range hist.GetBucket() {
				if target.Histogram.Bucket[i].GetUpperBound() != bucket.GetUpperBound() {
					return nil, errors.New("OTLP histogram bounds differ")
				}
				*target.Histogram.Bucket[i].CumulativeCount += bucket.GetCumulativeCount()
			}
		}
		out := &dto.MetricFamily{Name: proto.String(family.GetName()), Type: kind.Enum()}
		keys := make([]string, 0, len(groups))
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out.Metric = append(out.Metric, groups[key])
		}
		result = append(result, out)
	}
	return result, nil
}
