-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: additive NOT NULL columns with constant defaults. The
-- bounded lock timeout prevents startup from waiting on a long-running report.
-- Backfill: existing HTTP aggregates retain zero MCP outcomes by definition.
SET LOCAL lock_timeout = '1s';

ALTER TABLE usage_metric_buckets
    ADD COLUMN IF NOT EXISTS mcp_tool_calls BIGINT NOT NULL DEFAULT 0 CHECK (mcp_tool_calls >= 0),
    ADD COLUMN IF NOT EXISTS mcp_tool_failures BIGINT NOT NULL DEFAULT 0 CHECK (mcp_tool_failures >= 0);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SET LOCAL lock_timeout = '1s';
-- This migration is additive. Rollback retains the columns and historical
-- counters so an older binary can continue reading the same aggregate rows;
-- use a later ordered recovery migration if a schema change is required.
SELECT 1;

-- +goose StatementEnd
