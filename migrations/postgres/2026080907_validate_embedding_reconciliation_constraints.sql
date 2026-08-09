-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE embedding_jobs
    VALIDATE CONSTRAINT embedding_jobs_total_attempts_check;
ALTER TABLE embedding_jobs
    VALIDATE CONSTRAINT embedding_jobs_recovery_count_check;
ALTER TABLE embedding_jobs
    VALIDATE CONSTRAINT embedding_jobs_failure_class_check;
ALTER TABLE embedding_jobs
    VALIDATE CONSTRAINT embedding_jobs_failure_code_check;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $dense_mem_irreversible_embedding_reconciliation_validation$
BEGIN
    RAISE EXCEPTION '2026080907_validate_embedding_reconciliation_constraints is irreversible because validated constraints cannot be made NOT VALID';
END
$dense_mem_irreversible_embedding_reconciliation_validation$;

-- +goose StatementEnd
