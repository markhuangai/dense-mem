package postgres

import (
	"context"
	"database/sql"
	"errors"
)

func validateFeatureCatalog(ctx context.Context, tx *sql.Tx) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `WITH payloads AS (
	 SELECT c.oid,c.relrowsecurity,c.relforcerowsecurity,c.relname
	 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
	 WHERE n.nspname='public' AND c.relname=ANY(ARRAY['ontology_draft_envelopes',
	 'ontology_topic_revision_payloads','ontology_membership_revision_payloads','ontology_equivalence_revision_payloads','ontology_override_revision_payloads',
	 'ontology_topic_draft_payloads','ontology_membership_draft_payloads','ontology_equivalence_draft_payloads','ontology_override_draft_payloads']))
	 SELECT (SELECT count(*)=9 AND bool_and(relrowsecurity AND relforcerowsecurity) FROM payloads)
	 AND NOT EXISTS(SELECT 1 FROM payloads p WHERE
	 (SELECT count(*) FROM pg_policy policy WHERE policy.polrelid=p.oid AND policy.polname IN ('feature_read','feature_insert'))<>2
	 OR
	 NOT EXISTS(SELECT 1 FROM pg_trigger t WHERE t.tgrelid=p.oid AND t.tgname='feature_append_only' AND t.tgenabled='O')
	 OR NOT EXISTS(SELECT 1 FROM pg_constraint f WHERE f.conrelid=p.oid AND f.contype='f' AND f.convalidated AND f.confdeltype='r'
	 AND cardinality(f.conkey)=CASE WHEN p.relname LIKE '%_revision_payloads' THEN 5 ELSE 6 END))
	 AND (SELECT count(*)=2 AND bool_and('security_invoker=true'=ANY(c.reloptions)) FROM pg_class c
	 WHERE c.relnamespace='public'::regnamespace AND c.relname IN ('ontology_revision_payloads','ontology_draft_payloads'))
	 AND NOT EXISTS(SELECT 1 FROM information_schema.views WHERE table_schema='public'
	 AND table_name IN ('ontology_revision_payloads','ontology_draft_payloads') AND is_updatable<>'NO')
	 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
	 WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname NOT IN ('goose_db_version','data_migration_runs','data_migration_checkpoints')
	 AND NOT EXISTS(SELECT 1 FROM pg_trigger t WHERE t.tgrelid=c.oid AND t.tgname='feature_storage_write_gate' AND t.tgenabled='O'))`).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("feature_storage_blocked: feature catalog verification failed")
	}
	return nil
}
