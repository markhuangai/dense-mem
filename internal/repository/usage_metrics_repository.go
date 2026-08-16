package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// UsageMetricsRepository persists and reads bounded operational usage metrics.
type UsageMetricsRepository interface {
	UpsertBuckets(ctx context.Context, buckets []domain.UsageMetricBucket) error
	PruneBefore(ctx context.Context, cutoff time.Time) error
	Snapshot(ctx context.Context, filter domain.UsageMetricsFilter) (*domain.UsageMetricsSnapshot, error)
}

type UsageMetricsRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ UsageMetricsRepository = (*UsageMetricsRepositoryImpl)(nil)

func NewUsageMetricsRepository(db *gorm.DB, rls postgres.RLSHelper) *UsageMetricsRepositoryImpl {
	return &UsageMetricsRepositoryImpl{db: db, rls: rls}
}

func (r *UsageMetricsRepositoryImpl) UpsertBuckets(ctx context.Context, buckets []domain.UsageMetricBucket) error {
	if len(buckets) == 0 {
		return nil
	}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		for _, bucket := range buckets {
			if err := tx.Exec(`
				INSERT INTO usage_metric_buckets (
					bucket_start, team_id, key_id, route, method, status_class,
					request_count, error_count, total_latency_ms, max_latency_ms,
					last_seen_at, created_at, updated_at
				) VALUES (
					$1, $2, $3, $4, $5, $6,
					$7, $8, $9, $10,
					$11, now(), now()
				)
				ON CONFLICT (bucket_start, team_id, key_id, route, method, status_class)
				DO UPDATE SET
					request_count = usage_metric_buckets.request_count + EXCLUDED.request_count,
					error_count = usage_metric_buckets.error_count + EXCLUDED.error_count,
					total_latency_ms = usage_metric_buckets.total_latency_ms + EXCLUDED.total_latency_ms,
					max_latency_ms = GREATEST(usage_metric_buckets.max_latency_ms, EXCLUDED.max_latency_ms),
					last_seen_at = GREATEST(usage_metric_buckets.last_seen_at, EXCLUDED.last_seen_at),
					updated_at = now()
			`,
				bucket.BucketStart,
				bucket.TeamID,
				bucket.KeyID,
				bucket.Route,
				bucket.Method,
				bucket.StatusClass,
				bucket.RequestCount,
				bucket.ErrorCount,
				bucket.TotalLatencyMS,
				bucket.MaxLatencyMS,
				bucket.LastSeenAt,
			).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to upsert usage metric buckets: %w", err)
	}
	return nil
}

func (r *UsageMetricsRepositoryImpl) PruneBefore(ctx context.Context, cutoff time.Time) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec("DELETE FROM usage_metric_buckets WHERE bucket_start < $1", cutoff).Error
	})
	if err != nil {
		return fmt.Errorf("failed to prune usage metric buckets: %w", err)
	}
	return nil
}

func (r *UsageMetricsRepositoryImpl) Snapshot(ctx context.Context, filter domain.UsageMetricsFilter) (*domain.UsageMetricsSnapshot, error) {
	snapshot := &domain.UsageMetricsSnapshot{
		Teams:  []domain.UsageTeamMetric{},
		Keys:   []domain.UsageKeyMetric{},
		Routes: []domain.UsageRouteMetric{},
	}
	teamFilter := any(nil)
	if filter.TeamID != nil {
		teamFilter = filter.TeamID.String()
	}

	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		system, err := queryUsageTotal(tx, `
			SELECT
				COALESCE(SUM(request_count), 0),
				COALESCE(SUM(error_count), 0),
				COALESCE(SUM(total_latency_ms), 0),
				COALESCE(MAX(max_latency_ms), 0)
			FROM usage_metric_buckets b
			WHERE b.bucket_start >= $1
				AND b.bucket_start < $2
				AND ($3::uuid IS NULL OR b.team_id = $3::uuid)
		`, filter.From, filter.To, teamFilter)
		if err != nil {
			return err
		}
		snapshot.System = system

		teams, err := queryTeamUsage(tx, filter, teamFilter)
		if err != nil {
			return err
		}
		snapshot.Teams = teams

		keys, err := queryKeyUsage(tx, filter, teamFilter)
		if err != nil {
			return err
		}
		snapshot.Keys = keys

		routes, err := queryRouteUsage(tx, filter, teamFilter)
		if err != nil {
			return err
		}
		snapshot.Routes = routes
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read usage metrics snapshot: %w", err)
	}
	return snapshot, nil
}

func queryTeamUsage(tx *gorm.DB, filter domain.UsageMetricsFilter, teamFilter any) ([]domain.UsageTeamMetric, error) {
	rows, err := tx.Raw(`
		SELECT
			b.team_id::text,
			COALESCE(t.name, ''),
			COALESCE(SUM(b.request_count), 0),
			COALESCE(SUM(b.error_count), 0),
			COALESCE(SUM(b.total_latency_ms), 0),
			COALESCE(MAX(b.max_latency_ms), 0)
		FROM usage_metric_buckets b
		LEFT JOIN teams t ON t.id = b.team_id
		WHERE b.bucket_start >= $1
			AND b.bucket_start < $2
			AND ($3::uuid IS NULL OR b.team_id = $3::uuid)
		GROUP BY b.team_id, t.name
		ORDER BY COALESCE(SUM(b.request_count), 0) DESC, t.name ASC
		LIMIT 100
	`, filter.From, filter.To, teamFilter).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.UsageTeamMetric{}
	for rows.Next() {
		var (
			teamIDRaw string
			teamName  string
			requests  int64
			errors    int64
			totalMS   int64
			maxMS     int64
		)
		if err := rows.Scan(&teamIDRaw, &teamName, &requests, &errors, &totalMS, &maxMS); err != nil {
			return nil, err
		}
		teamID, err := uuid.Parse(teamIDRaw)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.UsageTeamMetric{
			TeamID:           teamID,
			TeamName:         teamName,
			UsageMetricTotal: totalFromSums(requests, errors, totalMS, maxMS),
		})
	}
	return out, rows.Err()
}

func queryKeyUsage(tx *gorm.DB, filter domain.UsageMetricsFilter, teamFilter any) ([]domain.UsageKeyMetric, error) {
	rows, err := tx.Raw(`
		SELECT
			b.team_id::text,
			COALESCE(t.name, ''),
			b.key_id::text,
			COALESCE(NULLIF(k.name, ''), NULLIF(owner_membership.sso_profile_name, ''), owner_actor.display_name, ''),
			COALESCE(k.key_suffix, ''),
			COALESCE(SUM(b.request_count), 0),
			COALESCE(SUM(b.error_count), 0),
			COALESCE(SUM(b.total_latency_ms), 0),
			COALESCE(MAX(b.max_latency_ms), 0)
		FROM usage_metric_buckets b
		LEFT JOIN teams t ON t.id = b.team_id
		LEFT JOIN credentials k
			ON k.id = b.key_id AND k.team_id = b.team_id
		LEFT JOIN ownership_aliases owner_alias
			ON owner_alias.team_id = b.team_id AND owner_alias.legacy_owner_id = b.key_id
		LEFT JOIN team_memberships owner_membership
			ON owner_membership.team_id = owner_alias.team_id
			AND owner_membership.actor_identity_id = owner_alias.canonical_identity_id
		LEFT JOIN actor_identities owner_actor ON owner_actor.id = owner_alias.canonical_identity_id
		WHERE b.bucket_start >= $1
			AND b.bucket_start < $2
			AND ($3::uuid IS NULL OR b.team_id = $3::uuid)
		GROUP BY b.team_id, t.name, b.key_id, k.name, k.key_suffix, owner_membership.sso_profile_name, owner_actor.display_name
		ORDER BY COALESCE(SUM(b.request_count), 0) DESC, COALESCE(k.name, owner_membership.sso_profile_name, owner_actor.display_name, '') ASC
		LIMIT 200
	`, filter.From, filter.To, teamFilter).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.UsageKeyMetric{}
	for rows.Next() {
		var (
			teamIDRaw string
			teamName  string
			keyIDRaw  string
			keyName   string
			keySuffix string
			requests  int64
			errors    int64
			totalMS   int64
			maxMS     int64
		)
		if err := rows.Scan(&teamIDRaw, &teamName, &keyIDRaw, &keyName, &keySuffix, &requests, &errors, &totalMS, &maxMS); err != nil {
			return nil, err
		}
		teamID, err := uuid.Parse(teamIDRaw)
		if err != nil {
			return nil, err
		}
		keyID, err := uuid.Parse(keyIDRaw)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.UsageKeyMetric{
			TeamID:           teamID,
			TeamName:         teamName,
			KeyID:            keyID,
			KeyName:          keyName,
			KeySuffix:        keySuffix,
			UsageMetricTotal: totalFromSums(requests, errors, totalMS, maxMS),
		})
	}
	return out, rows.Err()
}

func queryRouteUsage(tx *gorm.DB, filter domain.UsageMetricsFilter, teamFilter any) ([]domain.UsageRouteMetric, error) {
	rows, err := tx.Raw(`
		SELECT
			b.route,
			b.method,
			b.status_class,
			COALESCE(SUM(b.request_count), 0),
			COALESCE(SUM(b.error_count), 0),
			COALESCE(SUM(b.total_latency_ms), 0),
			COALESCE(MAX(b.max_latency_ms), 0)
		FROM usage_metric_buckets b
		WHERE b.bucket_start >= $1
			AND b.bucket_start < $2
			AND ($3::uuid IS NULL OR b.team_id = $3::uuid)
		GROUP BY b.route, b.method, b.status_class
		ORDER BY COALESCE(SUM(b.request_count), 0) DESC, b.route ASC, b.method ASC, b.status_class ASC
		LIMIT 200
	`, filter.From, filter.To, teamFilter).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.UsageRouteMetric{}
	for rows.Next() {
		var (
			route       string
			method      string
			statusClass int
			requests    int64
			errors      int64
			totalMS     int64
			maxMS       int64
		)
		if err := rows.Scan(&route, &method, &statusClass, &requests, &errors, &totalMS, &maxMS); err != nil {
			return nil, err
		}
		out = append(out, domain.UsageRouteMetric{
			Route:            route,
			Method:           method,
			StatusClass:      fmt.Sprintf("%dxx", statusClass),
			UsageMetricTotal: totalFromSums(requests, errors, totalMS, maxMS),
		})
	}
	return out, rows.Err()
}

func queryUsageTotal(tx *gorm.DB, query string, args ...any) (domain.UsageMetricTotal, error) {
	row := tx.Raw(query, args...).Row()
	var requests, errors, totalMS, maxMS sql.NullInt64
	if err := row.Scan(&requests, &errors, &totalMS, &maxMS); err != nil {
		return domain.UsageMetricTotal{}, err
	}
	return totalFromSums(nullInt64(requests), nullInt64(errors), nullInt64(totalMS), nullInt64(maxMS)), nil
}

func totalFromSums(requests, errors, totalLatencyMS, maxLatencyMS int64) domain.UsageMetricTotal {
	total := domain.UsageMetricTotal{
		Requests:     requests,
		Errors:       errors,
		MaxLatencyMS: maxLatencyMS,
	}
	if requests > 0 {
		total.AvgLatencyMS = float64(totalLatencyMS) / float64(requests)
	}
	return total
}

func nullInt64(value sql.NullInt64) int64 {
	if value.Valid {
		return value.Int64
	}
	return 0
}

func (r *UsageMetricsRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithSystemTx(ctx, r.db, fn)
	}
	return fn(r.db.WithContext(ctx))
}
