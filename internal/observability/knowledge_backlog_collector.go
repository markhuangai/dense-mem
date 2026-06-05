package observability

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	defaultKnowledgeBacklogTimeout = 2 * time.Second
	defaultKnowledgeBacklogTTL     = 30 * time.Second
)

var allowedKnowledgeBacklogItems = map[string]struct{}{
	"claim_candidate":         {},
	"claim_validated":         {},
	"claim_disputed":          {},
	"fact_needs_revalidation": {},
}

// KnowledgeBacklogReader is the read-only Neo4j surface required by the
// backlog collector. It intentionally matches the production client without
// exposing raw sessions.
type KnowledgeBacklogReader interface {
	ExecuteRead(ctx context.Context, fn neo4j.ManagedTransactionWork) (any, error)
}

type knowledgeBacklogSample struct {
	TeamID    string
	ProfileID string
	Item      string
	Count     int64
}

// KnowledgeBacklogCollector exports current claim/fact backlog gauges.
type KnowledgeBacklogCollector struct {
	reader  KnowledgeBacklogReader
	logger  LogProvider
	timeout time.Duration
	ttl     time.Duration
	desc    *prometheus.Desc

	mu       sync.Mutex
	cachedAt time.Time
	cached   []knowledgeBacklogSample
}

// NewKnowledgeBacklogCollector constructs a collector that counts indexed
// backlog statuses from Neo4j and caches results briefly between scrapes.
func NewKnowledgeBacklogCollector(reader KnowledgeBacklogReader, logger LogProvider) *KnowledgeBacklogCollector {
	return &KnowledgeBacklogCollector{
		reader:  reader,
		logger:  logger,
		timeout: defaultKnowledgeBacklogTimeout,
		ttl:     defaultKnowledgeBacklogTTL,
		desc: prometheus.NewDesc(
			"densemem_knowledge_backlog_items",
			"Current knowledge backlog items by bounded item type.",
			append(identityLabels(), "item"),
			nil,
		),
	}
}

// RegisterKnowledgeBacklogCollector attaches the backlog collector to this
// private registry. Nil readers are ignored so startup wiring can stay simple.
func (m *PrometheusMetrics) RegisterKnowledgeBacklogCollector(reader KnowledgeBacklogReader, logger LogProvider) {
	if m == nil || reader == nil {
		return
	}
	m.registry.MustRegister(NewKnowledgeBacklogCollector(reader, logger))
}

func (c *KnowledgeBacklogCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *KnowledgeBacklogCollector) Collect(ch chan<- prometheus.Metric) {
	samples, err := c.samples()
	if err != nil {
		if c.logger != nil {
			c.logger.Warn("knowledge backlog metrics collection failed", String("error", err.Error()))
		}
		return
	}
	for _, sample := range samples {
		if _, ok := allowedKnowledgeBacklogItems[sample.Item]; !ok {
			continue
		}
		if sample.Count < 0 {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			float64(sample.Count),
			normalizeLabel(sample.TeamID),
			normalizeLabel(sample.ProfileID),
			sample.Item,
		)
	}
}

func (c *KnowledgeBacklogCollector) samples() ([]knowledgeBacklogSample, error) {
	c.mu.Lock()
	if c.ttl > 0 && len(c.cached) > 0 && time.Since(c.cachedAt) < c.ttl {
		out := cloneKnowledgeBacklogSamples(c.cached)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultKnowledgeBacklogTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	raw, err := c.reader.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, knowledgeBacklogQuery, nil)
		if err != nil {
			return nil, err
		}
		records, err := result.Collect(ctx)
		if err != nil {
			return nil, err
		}
		samples := make([]knowledgeBacklogSample, 0, len(records))
		for _, record := range records {
			sample, err := knowledgeBacklogSampleFromMap(record.AsMap())
			if err != nil {
				return nil, err
			}
			samples = append(samples, sample)
		}
		if _, err := result.Consume(ctx); err != nil {
			return nil, err
		}
		return samples, nil
	})
	if err != nil {
		c.mu.Lock()
		if len(c.cached) > 0 {
			out := cloneKnowledgeBacklogSamples(c.cached)
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()
		return nil, err
	}

	samples, ok := raw.([]knowledgeBacklogSample)
	if !ok {
		return nil, fmt.Errorf("knowledge backlog query returned %T", raw)
	}
	c.mu.Lock()
	c.cachedAt = time.Now()
	c.cached = cloneKnowledgeBacklogSamples(samples)
	c.mu.Unlock()
	return samples, nil
}

func knowledgeBacklogSampleFromMap(row map[string]any) (knowledgeBacklogSample, error) {
	count, ok := int64FromAny(row["count"])
	if !ok {
		return knowledgeBacklogSample{}, fmt.Errorf("invalid backlog count %v", row["count"])
	}
	return knowledgeBacklogSample{
		TeamID:    stringFromAny(row["team_id"]),
		ProfileID: stringFromAny(row["profile_id"]),
		Item:      stringFromAny(row["item"]),
		Count:     count,
	}, nil
}

func stringFromAny(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func int64FromAny(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

func cloneKnowledgeBacklogSamples(in []knowledgeBacklogSample) []knowledgeBacklogSample {
	out := make([]knowledgeBacklogSample, len(in))
	copy(out, in)
	return out
}

const knowledgeBacklogQuery = `
CALL {
    MATCH (c:Claim)
    WHERE c.status = 'candidate'
    RETURN c.team_id AS team_id,
           coalesce(c.owner_profile_id, c.created_by_profile_id, 'unknown') AS profile_id,
           'claim_candidate' AS item,
           count(c) AS count
    UNION ALL
    MATCH (c:Claim)
    WHERE c.status = 'validated'
    RETURN c.team_id AS team_id,
           coalesce(c.owner_profile_id, c.created_by_profile_id, 'unknown') AS profile_id,
           'claim_validated' AS item,
           count(c) AS count
    UNION ALL
    MATCH (c:Claim)
    WHERE c.status = 'disputed'
    RETURN c.team_id AS team_id,
           coalesce(c.owner_profile_id, c.created_by_profile_id, 'unknown') AS profile_id,
           'claim_disputed' AS item,
           count(c) AS count
    UNION ALL
    MATCH (f:Fact)
    WHERE f.status = 'needs_revalidation'
    RETURN f.team_id AS team_id,
           coalesce(f.owner_profile_id, f.created_by_profile_id, 'unknown') AS profile_id,
           'fact_needs_revalidation' AS item,
           count(f) AS count
}
RETURN team_id, profile_id, item, count`
