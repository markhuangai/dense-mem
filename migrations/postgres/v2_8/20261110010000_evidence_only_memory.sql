-- Lock/rewrite impact: stopped-service transaction locks the explicit inventory; five-second acquisition bound, no provider calls.
-- RLS impact: temporary migration policies are removed before commit; FORCE RLS and runtime immutability remain enabled.
-- Backfill: reset derived ontology for normal Evidence rediscovery; retire graph payloads and unfinished extraction checkpoints, retaining Evidence and operation identity.
-- Backward compatibility: stop incompatible instances and verify a matching restore-tested backup before normal startup.
-- Rollback: before commit the transaction rolls back; after commit restore that backup, and after new writes recover forward unless separately approved.
-- Inventory identity: sha256:b0d41120350071869ff9c3d77c6ddbb562341c41c06422f173b30cb4958df754
-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);
SELECT set_config('app.current_team_id','',true);
SELECT set_config('app.current_profile_id','',true);
CREATE TEMP TABLE evidence_only_progress_state(sequence bigint NOT NULL, emitted_at timestamptz NOT NULL, discovered bigint NOT NULL) ON COMMIT DROP;
INSERT INTO evidence_only_progress_state VALUES(0,clock_timestamp(),0);
CREATE OR REPLACE FUNCTION pg_temp.evidence_only_progress(p_phase text,p_work text,p_state text,p_metric text,p_unit text,
 p_completed bigint DEFAULT NULL,p_total bigint DEFAULT NULL,p_discovered bigint DEFAULT NULL,p_iterations bigint DEFAULT NULL)
 RETURNS void LANGUAGE plpgsql AS $progress$
DECLARE execution_id text:=current_setting('app.migration_execution_id',true); next_sequence bigint; previous_at timestamptz;
BEGIN
 IF COALESCE(execution_id,'')='' THEN RETURN; END IF;
 SELECT emitted_at INTO previous_at FROM evidence_only_progress_state;
 IF p_state='progress' AND clock_timestamp()-previous_at<interval '1 second' THEN RETURN; END IF;
 UPDATE evidence_only_progress_state SET sequence=sequence+1,emitted_at=clock_timestamp(),discovered=COALESCE(p_discovered,discovered)
 RETURNING sequence INTO next_sequence;
 RAISE NOTICE 'dense_mem_migration_progress:%:%',execution_id,jsonb_build_object(
  'sequence',next_sequence,'phase',p_phase,'work_unit',p_work,'state',p_state,'metric',p_metric,'unit',p_unit,
  'completed',p_completed,'total',p_total,'discovered',p_discovered,'iterations',p_iterations);
END $progress$;
DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('sql_stream','migration','started','completed','batches'); END $progress$;


DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('migration_context','private_memory_legal_holds','started','completed','tables',NULL,NULL,NULL,NULL); END $progress$;
LOCK TABLE private_memory_legal_holds, memory_spaces IN SHARE ROW EXCLUSIVE MODE;
DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('migration_context','private_memory_legal_holds','completed','completed','tables',2,2,NULL,NULL); END $progress$;

CREATE TEMP TABLE evidence_only_inventory(name text PRIMARY KEY, disposition text NOT NULL, row_count bigint NOT NULL DEFAULT 0) ON COMMIT DROP;
INSERT INTO evidence_only_inventory SELECT unnest(ARRAY['community_memberships','community_records','community_snapshot_runs','community_sources','community_summary_attempts','community_topic_dependencies','community_topic_discovery','community_topic_versions','community_topic_work','dream_cycle_runs','dream_diagnostic_captures','dream_evidence_target_attempts','dream_evidence_target_evaluations','dream_path_evaluation_run_links','dream_path_evaluations','entity_correction_events','entity_correction_plans','entity_names','entity_records','entity_resolution_events','hypotheses','hypothesis_derivation_sources','hypothesis_evidence_derivation_sources','hypothesis_feedback_events','predicate_definitions','predicate_registration_events','relationship_conflict_ai_assessment_attempts','relationship_conflict_ai_assessment_events','relationship_conflict_cases','relationship_conflict_derived_evidence_tasks','relationship_conflict_events','relationship_conflict_evidence_derivations','relationship_conflict_position_members','relationship_conflict_positions','relationship_conflict_resolution_plans','relationship_conflict_review_runs','relationship_correction_events','relationship_correction_submissions','relationship_cross_references','relationship_evidence_supports','relationship_observations','relationship_records','relationship_support_decision_events','relationship_transition_events','submission_relationship_results','team_predicate_definitions','value_records','verification_events','review_tasks','placement_assessments','placement_items','placement_outcomes','placement_runs']::text[]),'remove';
INSERT INTO evidence_only_inventory SELECT unnest(ARRAY['evidence_sources','evidence_source_revisions','evidence_exact_aliases','evidence_occurrences','evidence_fragments','evidence_security_events','evidence_security_signals','evidence_quarantines','evidence_lifecycle_operations','evidence_lifecycle_events','evidence_conflict_cases','evidence_conflict_positions','evidence_conflict_events']::text[]),'retain';
INSERT INTO evidence_only_inventory SELECT unnest(ARRAY['ontology_catalog_heads','ontology_publications','ontology_record_heads','ontology_record_revisions','ontology_source_dependencies','ontology_revision_dependencies','ontology_assessments','ontology_draft_heads','ontology_draft_revisions','ontology_maintenance_sources','ontology_maintenance_markers']::text[]),'reset';
INSERT INTO evidence_only_inventory SELECT unnest(ARRAY['knowledge_ingests','remember_attempts','remember_attempt_events','remember_attempt_diagnostics','remember_failure_artifacts','remember_invocation_diagnostics','semantic_assessments','session_submissions','session_events','session_extraction_checkpoints','session_submission_receipts','session_submission_diagnostics','ontology_maintenance_teams','ontology_maintenance_batches','recall_feedback_events','audit_log','operation_logs','search_documents','search_projection_generations','private_memory_migration_records','private_memory_migration_constraints','app_config']::text[]),'mixed';
CREATE TEMP TABLE evidence_only_triggers(table_name text, trigger_name text) ON COMMIT DROP;
CREATE TEMP TABLE evidence_only_fingerprints(table_name text, fingerprint text, PRIMARY KEY(table_name,fingerprint)) ON COMMIT DROP;
CREATE TEMP TABLE evidence_only_after(LIKE evidence_only_fingerprints INCLUDING ALL) ON COMMIT DROP;

DO $inventory$
DECLARE item record; held bigint; progress_rows bigint; unknown_dependencies text;
BEGIN
 FOR item IN SELECT * FROM evidence_only_inventory ORDER BY name LOOP
  IF to_regclass('public.'||item.name) IS NULL THEN
   IF item.disposition<>'remove' THEN RAISE EXCEPTION 'evidence_only_migration_blocked: missing inventory table %',item.name; END IF;
   CONTINUE;
  END IF;
  PERFORM pg_temp.evidence_only_progress('inventory_lock',item.name,'started','completed','tables',NULL,NULL,NULL,NULL);
  EXECUTE format('LOCK TABLE %I IN ACCESS EXCLUSIVE MODE',item.name);
  PERFORM pg_temp.evidence_only_progress('inventory_lock',item.name,'completed','completed','tables',1,1,NULL,NULL);
  EXECUTE format('CREATE POLICY evidence_only_inventory_read ON %I FOR SELECT USING(current_setting(''app.tx_mode'',true)=''migration'')',item.name);
 END LOOP;
 FOR item IN SELECT * FROM evidence_only_inventory WHERE to_regclass('public.'||name) IS NOT NULL ORDER BY name LOOP
  PERFORM pg_temp.evidence_only_progress('inventory_count',item.name,'started','counted','rows',NULL,NULL,NULL,NULL);
  EXECUTE format('SELECT count(*) FROM %I',item.name) INTO held;
  PERFORM pg_temp.evidence_only_progress('inventory_count',item.name,'completed','counted','rows',held,held,NULL,NULL);
  UPDATE evidence_only_inventory SET row_count=held WHERE name=item.name;
  IF item.disposition='retain' THEN
   PERFORM pg_temp.evidence_only_progress('fingerprint_before',item.name,'started','affected','rows',NULL,held,NULL,NULL);
   EXECUTE format('INSERT INTO evidence_only_fingerprints SELECT %L,encode(sha256(convert_to(to_jsonb(row_value)::text,''UTF8'')),''hex'') FROM %I row_value',item.name,item.name);
   GET DIAGNOSTICS progress_rows=ROW_COUNT;
   PERFORM pg_temp.evidence_only_progress('fingerprint_before',item.name,'completed','affected','rows',progress_rows,held,NULL,NULL);
  ELSE
   PERFORM pg_temp.evidence_only_progress('legal_holds',item.name,'started','matched','rows',NULL,NULL,NULL,NULL);
   EXECUTE format($held$SELECT count(*) FROM %I row_value WHERE
    COALESCE((to_jsonb(row_value)->>'retained_by_legal_hold')::boolean,false)
    OR dense_mem_private_space_held(NULLIF(to_jsonb(row_value)->>'space_id','')::uuid)
    OR dense_mem_private_space_held(NULLIF(to_jsonb(row_value)->>'memory_space_id','')::uuid)
    OR EXISTS(SELECT 1 FROM remember_attempts attempt
     WHERE attempt.team_id=NULLIF(to_jsonb(row_value)->>'team_id','')::uuid
      AND attempt.attempt_id=NULLIF(to_jsonb(row_value)->>'attempt_id','')::uuid
      AND dense_mem_private_space_held(attempt.space_id))$held$,item.name) INTO held;
   PERFORM pg_temp.evidence_only_progress('legal_holds',item.name,'completed','matched','rows',held,NULL,NULL,NULL);
   IF held>0 THEN RAISE EXCEPTION 'evidence_only_migration_blocked: legal hold table=% rows=%',item.name,held; END IF;
  END IF;
 END LOOP;
 PERFORM pg_temp.evidence_only_progress('dependency_check','constraints','started','completed','dependencies',NULL,NULL,NULL,NULL);
 SELECT string_agg(child.relname||'.'||constraint_row.conname,', ' ORDER BY child.relname,constraint_row.conname)
 INTO unknown_dependencies FROM pg_constraint constraint_row
 JOIN pg_class child ON child.oid=constraint_row.conrelid
 JOIN pg_class parent ON parent.oid=constraint_row.confrelid
 WHERE constraint_row.contype='f' AND parent.relnamespace='public'::regnamespace AND child.relnamespace='public'::regnamespace
 AND ((parent.relname IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove')
  AND child.relname NOT IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove'))
 OR (parent.relname IN (SELECT name FROM evidence_only_inventory WHERE disposition='reset')
  AND child.relname NOT IN (SELECT name FROM evidence_only_inventory WHERE disposition IN ('remove','reset'))));
 IF unknown_dependencies IS NOT NULL THEN RAISE EXCEPTION 'evidence_only_migration_blocked: unhandled retained dependency %',unknown_dependencies; END IF;
 PERFORM pg_temp.evidence_only_progress('dependency_check','constraints','completed','completed','dependencies',NULL,NULL,NULL,NULL);
END $inventory$;

-- Only inventory-owned guards and policies are opened, inside this transaction; their definitions are restored below.

DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('guards','policies','started','completed','tables',NULL,NULL,NULL,NULL); END $progress$;
DO $guards$
DECLARE item record; guard record;
BEGIN
 FOR item IN SELECT name FROM evidence_only_inventory WHERE disposition='mixed' LOOP
  EXECUTE format('CREATE POLICY evidence_only_migration_repair ON %I FOR ALL USING(current_setting(''app.tx_mode'',true)=''migration'') WITH CHECK(current_setting(''app.tx_mode'',true)=''migration'')',item.name);
  FOR guard IN SELECT tgname FROM pg_trigger WHERE tgrelid=to_regclass('public.'||item.name) AND NOT tgisinternal AND tgenabled<>'D' LOOP
   INSERT INTO evidence_only_triggers VALUES(item.name,guard.tgname);
   EXECUTE format('ALTER TABLE %I DISABLE TRIGGER %I',item.name,guard.tgname);
  END LOOP;
 END LOOP;
END $guards$;
DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('guards','policies','completed','completed','tables',NULL,NULL,NULL,NULL); END $progress$;


CREATE FUNCTION pg_temp.evidence_only_result(body jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT (COALESCE(body,'{}'::jsonb)-ARRAY['relationships','entities','values','predicates','predicate_registrations','relationship_results','relationship_conflicts','entity_resolutions','hypotheses','dreams','communities','community_coverage'])
 || CASE WHEN body ? 'conflicts' THEN jsonb_build_object('conflicts',COALESCE((SELECT jsonb_agg(conflict ORDER BY ordinal) FROM jsonb_array_elements(body->'conflicts') WITH ORDINALITY entries(conflict,ordinal) WHERE conflict->>'kind'='evidence_conflict'),'[]'::jsonb)) ELSE '{}'::jsonb END
 || jsonb_build_object('retired_payload',true,'retirement_reason','factual_graph_removed');
$$;
CREATE FUNCTION pg_temp.evidence_only_assessment(body jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT (COALESCE(body,'{}'::jsonb)-ARRAY['entity_resolutions','entity_results','predicate_registrations','relationship_results','relationship_conflict_results','relationships','entities','values','predicates','relationship_conflicts','graph_conflicts'])
 || jsonb_build_object('retired_payload',true,'retirement_reason','factual_graph_removed');
$$;
CREATE FUNCTION pg_temp.evidence_only_request(body bytea) RETURNS bytea LANGUAGE plpgsql AS $$
DECLARE object jsonb;
BEGIN
 IF octet_length(body)=0 THEN RETURN body; END IF;
 BEGIN object:=convert_from(body,'UTF8')::jsonb;
 EXCEPTION WHEN OTHERS THEN RETURN convert_to('{"retired_payload":true,"availability":"legacy_payload_unavailable"}','UTF8'); END;
 IF jsonb_typeof(object)<>'object' THEN RETURN convert_to('{"retired_payload":true,"availability":"legacy_payload_unavailable"}','UTF8'); END IF;
 RETURN convert_to((object-ARRAY['entities','values','relationships','predicates','proposal','predicate_registrations','known_relationship_ids','known_entity_ids'] || jsonb_build_object('retired_payload',true))::text,'UTF8');
END $$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','knowledge_ingests','started','affected','rows',NULL,NULL,NULL,NULL);


UPDATE knowledge_ingests SET metadata=metadata || jsonb_build_object('retired_payload',true,'retirement_reason','factual_graph_removed','original_status',status)
 WHERE COALESCE(metadata->>'contract_version','')<>'dense-mem.v2.8.0';
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','knowledge_ingests','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
ALTER TABLE knowledge_ingests DROP COLUMN proposal;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_attempts','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE remember_attempts SET public_result=pg_temp.evidence_only_result(public_result);
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_attempts','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
ALTER TABLE remember_attempts DROP CONSTRAINT remember_attempts_counts_check;
ALTER TABLE remember_attempts DROP COLUMN relationship_count;
ALTER TABLE remember_attempts ADD CONSTRAINT remember_attempts_counts_check CHECK(evidence_count>=0 AND document_count>=0 AND assessor_turns>=0 AND duration_ms>=0);
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_attempt_events','started','affected','rows',NULL,NULL,NULL,NULL);

-- The original submission kind remains accounting history, never an executable contract.
UPDATE remember_attempt_events SET metadata=pg_temp.evidence_only_result(metadata);
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_attempt_events','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','semantic_assessments','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE semantic_assessments SET response_history=COALESCE((SELECT jsonb_agg(
 CASE WHEN entry ? 'normalized_response' THEN jsonb_set(entry,'{normalized_response}',pg_temp.evidence_only_assessment(entry->'normalized_response')) ELSE pg_temp.evidence_only_assessment(entry) END ORDER BY ordinal)
 FROM jsonb_array_elements(response_history) WITH ORDINALITY AS entries(entry,ordinal)),'[]'::jsonb);
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','semantic_assessments','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_attempt_diagnostics','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE remember_attempt_diagnostics SET
 request_bytes=CASE WHEN kind='original_request' THEN pg_temp.evidence_only_request(request_bytes) ELSE convert_to('{"retired_payload":true,"availability":"legacy_provider_payload_unavailable"}','UTF8') END,
 response_bytes=CASE WHEN kind='caller_response' THEN pg_temp.evidence_only_request(response_bytes) ELSE convert_to('{"retired_payload":true,"availability":"legacy_provider_payload_unavailable"}','UTF8') END,
 capture_state='not_captured';
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_attempt_diagnostics','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_invocation_diagnostics','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE remember_invocation_diagnostics SET request_bytes=pg_temp.evidence_only_request(request_bytes),response_bytes=pg_temp.evidence_only_request(response_bytes),
 request_capture_state='unavailable',response_capture_state='unavailable',request_capture_reason='legacy_graph_payload_retired',response_capture_reason='legacy_graph_payload_retired',
 provider_exchanges=COALESCE((SELECT jsonb_agg((exchange-ARRAY['request_body','response_body','request_bytes','response_bytes','RequestBody','ResponseBody','ResponseBodyProjection'])||jsonb_build_object('retired_payload',true,'availability','legacy_provider_payload_unavailable') ORDER BY ordinal)
 FROM jsonb_array_elements(provider_exchanges) WITH ORDINALITY entries(exchange,ordinal)),'[]'::jsonb);
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_invocation_diagnostics','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_failure_artifacts','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE remember_failure_artifacts SET content_bytes=convert_to('{"retired_payload":true,"availability":"legacy_failure_payload_unavailable"}','UTF8'),
 byte_count=octet_length(convert_to('{"retired_payload":true,"availability":"legacy_failure_payload_unavailable"}','UTF8')),
 content_sha256='sha256:'||encode(sha256(convert_to('{"retired_payload":true,"availability":"legacy_failure_payload_unavailable"}','UTF8')),'hex');
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','remember_failure_artifacts','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;

CREATE FUNCTION pg_temp.evidence_only_session_result(body jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT pg_temp.evidence_only_result(body)||jsonb_build_object('original_contract_version',body->>'contract_version','contract_version','dense-mem.v2.8.0','memory_outcome','retired_contract');
$$;

CREATE TEMP TABLE evidence_only_sessions ON COMMIT DROP AS
 SELECT team_id,space_id,submission_id,COALESCE(intake->>'ExtractionVersion','') AS original_version,
 COALESCE(result->>'processing_state','unfinished') AS original_outcome,
 (result IS NULL OR (result->>'processing_state'='failed' AND EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(result->'errors','[]'::jsonb)) error WHERE error->>'retryable'='true')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(result->'errors','[]'::jsonb)) error WHERE error->>'code' IN ('submission_policy_rejected','stale_input','input_budget_exceeded')))) AS resumable,
 (SELECT count(*) FROM session_extraction_checkpoints checkpoint WHERE checkpoint.team_id=submission.team_id AND checkpoint.space_id=submission.space_id AND checkpoint.submission_id=submission.submission_id) AS cleared_checkpoints
 FROM session_submissions submission WHERE COALESCE(intake->>'ExtractionVersion','')<>'session_evidence_excerpts_v2';
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_extraction_checkpoints','started','affected','rows',NULL,NULL,NULL,NULL);

DELETE FROM session_extraction_checkpoints checkpoint USING evidence_only_sessions legacy
 WHERE checkpoint.team_id=legacy.team_id AND checkpoint.space_id=legacy.space_id AND checkpoint.submission_id=legacy.submission_id
 AND (legacy.resumable OR checkpoint.window_index=-1);
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_extraction_checkpoints','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_extraction_checkpoints','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE session_extraction_checkpoints SET body=pg_temp.evidence_only_assessment(body)-ARRAY['entities','relationships','groups']||jsonb_build_object('availability','retired_contract');
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_extraction_checkpoints','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_submissions','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE session_submissions submission SET intake=CASE WHEN legacy.resumable THEN jsonb_set(intake,'{ExtractionVersion}','"session_evidence_excerpts_v2"'::jsonb)
 ||jsonb_build_object('EvidenceOnlyResume',jsonb_build_object('original_version',legacy.original_version,'original_outcome',legacy.original_outcome,'cleared_checkpoints',legacy.cleared_checkpoints)) ELSE intake END,
 result=CASE WHEN legacy.resumable THEN NULL ELSE pg_temp.evidence_only_session_result(result) END
 FROM evidence_only_sessions legacy WHERE submission.team_id=legacy.team_id AND submission.space_id=legacy.space_id AND submission.submission_id=legacy.submission_id;
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_submissions','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_submission_receipts','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE session_submission_receipts SET body=pg_temp.evidence_only_session_result(body);
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_submission_receipts','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_submission_diagnostics','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE session_submission_diagnostics SET body=jsonb_build_object('capture_state','unavailable','retired_payload',true,'retirement_reason','factual_graph_removed',
 'payload',jsonb_build_object('tool',body->'payload'->'tool','request',body->'payload'->'request','result',pg_temp.evidence_only_result(body->'payload'->'result'),
 'exchanges',COALESCE((SELECT jsonb_agg((exchange-ARRAY['RequestBody','ResponseBody','ResponseBodyProjection'])||jsonb_build_object('retired_payload',true,'availability','legacy_provider_payload_unavailable') ORDER BY ordinal)
 FROM jsonb_array_elements(COALESCE(NULLIF(body->'payload'->'exchanges','null'::jsonb),'[]'::jsonb)) WITH ORDINALITY entries(exchange,ordinal)),'[]'::jsonb)));
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('session_cleanup','session_submission_diagnostics','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
ALTER TABLE session_extraction_checkpoints DROP CONSTRAINT session_extraction_checkpoints_window_index_check;
ALTER TABLE session_extraction_checkpoints ADD CONSTRAINT session_extraction_checkpoints_window_index_check CHECK(window_index BETWEEN 0 AND 7);

-- Reset derived organization together with its legacy FK child; RESTRICT refuses unlisted dependencies.
DO $ontology_reset$
DECLARE tables text; reset_rows bigint;
BEGIN
 SELECT string_agg(format('public.%I',name),', ' ORDER BY name),sum(row_count)
 INTO tables,reset_rows FROM evidence_only_inventory
 WHERE (disposition='reset' OR name='community_topic_work') AND to_regclass('public.'||name) IS NOT NULL;
 PERFORM pg_temp.evidence_only_progress('ontology_reset','ontology_catalog_heads','started','affected','rows',0,reset_rows,NULL,NULL);
 EXECUTE 'TRUNCATE TABLE '||tables||' RESTRICT';
 PERFORM pg_temp.evidence_only_progress('ontology_reset','ontology_catalog_heads','completed','affected','rows',reset_rows,reset_rows,NULL,NULL);
END $ontology_reset$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('ontology_cleanup','ontology_maintenance_teams','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE ontology_maintenance_teams SET discovery_kind=0,discovery_cursor='',lease_token=NULL,lease_until=NULL;
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('ontology_cleanup','ontology_maintenance_teams','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
ALTER TABLE ontology_maintenance_batches DROP CONSTRAINT ontology_maintenance_batches_sources_check;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('ontology_cleanup','ontology_maintenance_batches','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE ontology_maintenance_batches SET sources=COALESCE((SELECT jsonb_agg(source) FROM jsonb_array_elements(sources) source WHERE source->>'kind'='evidence'),'[]'::jsonb),
 revisions=jsonb_build_object('retired_contract',true,'original_status',status),
 status=CASE WHEN status='running' THEN 'lost' ELSE status END,
 failure_code=CASE WHEN status='running' THEN 'retired_contract' ELSE failure_code END,
 completed_at=CASE WHEN status='running' THEN clock_timestamp() ELSE completed_at END;
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('ontology_cleanup','ontology_maintenance_batches','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
ALTER TABLE ontology_maintenance_batches ADD CONSTRAINT ontology_maintenance_batches_sources_check CHECK(jsonb_typeof(sources)='array' AND jsonb_array_length(sources)<=20);
ALTER TABLE ontology_maintenance_teams DROP CONSTRAINT ontology_maintenance_teams_discovery_kind_check;
ALTER TABLE ontology_maintenance_teams ADD CONSTRAINT ontology_maintenance_teams_discovery_kind_check CHECK(discovery_kind BETWEEN 0 AND 1);
ALTER TABLE ontology_record_heads DROP CONSTRAINT ontology_record_heads_kind_check;
ALTER TABLE ontology_record_revisions DROP CONSTRAINT ontology_record_revisions_kind_check;
ALTER TABLE ontology_record_heads ADD CONSTRAINT ontology_record_heads_kind_check CHECK(kind IN ('topic','assignment','evidence_group','override'));
ALTER TABLE ontology_record_revisions ADD CONSTRAINT ontology_record_revisions_kind_check CHECK(kind IN ('topic','assignment','evidence_group','override'));
ALTER TABLE ontology_source_dependencies DROP CONSTRAINT ontology_source_dependencies_source_kind_check;
ALTER TABLE ontology_source_dependencies ADD CONSTRAINT ontology_source_dependencies_source_kind_check CHECK(source_kind='evidence');
ALTER TABLE ontology_maintenance_sources DROP CONSTRAINT ontology_maintenance_sources_source_kind_check;
ALTER TABLE ontology_maintenance_sources ADD CONSTRAINT ontology_maintenance_sources_source_kind_check CHECK(source_kind='evidence');

CREATE FUNCTION pg_temp.evidence_only_feedback_refs(body jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(jsonb_agg(CASE WHEN ref->>'type'='fragment' THEN jsonb_set(ref,'{type}','"evidence"'::jsonb) ELSE ref END ORDER BY ordinal),'[]'::jsonb)
 FROM jsonb_array_elements(COALESCE(body,'[]'::jsonb)) WITH ORDINALITY entries(ref,ordinal)
 WHERE ref->>'type' IN ('fragment','evidence','topic');
$$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','recall_feedback_events','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE recall_feedback_events SET result_refs=pg_temp.evidence_only_feedback_refs(result_refs),
 tool_args=tool_args-ARRAY['use_communities','known_relationship_ids','relationship_ids'],
 snapshot_metadata=snapshot_metadata||jsonb_build_object('retired_payload',true);
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','recall_feedback_events','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','recall_feedback_events','started','affected','rows',NULL,NULL,NULL,NULL);

UPDATE recall_feedback_events SET result_count=jsonb_array_length(result_refs),irrelevant_result_refs=pg_temp.evidence_only_feedback_refs(irrelevant_result_refs);
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','recall_feedback_events','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
ALTER TABLE recall_feedback_events DROP COLUMN dream_feedback;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','search_documents','started','affected','rows',NULL,NULL,NULL,NULL);

DELETE FROM search_documents WHERE source_kind<>'evidence';
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','search_documents','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','search_projection_generations','started','affected','rows',NULL,NULL,NULL,NULL);

DELETE FROM search_projection_generations WHERE source_kind<>'evidence';
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','search_projection_generations','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
ALTER TABLE search_documents DROP CONSTRAINT search_documents_source_kind_check;
ALTER TABLE search_documents ADD CONSTRAINT search_documents_source_kind_check CHECK(source_kind='evidence');
ALTER TABLE search_projection_generations DROP CONSTRAINT search_projection_generations_source_kind_check;
ALTER TABLE search_projection_generations ADD CONSTRAINT search_projection_generations_source_kind_check CHECK(source_kind='evidence');
DROP INDEX IF EXISTS search_documents_relationship_projection_idx;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','private_memory_migration_records','started','affected','rows',NULL,NULL,NULL,NULL);

DELETE FROM private_memory_migration_records WHERE source_table IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove');
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','private_memory_migration_records','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','private_memory_migration_constraints','started','affected','rows',NULL,NULL,NULL,NULL);

DELETE FROM private_memory_migration_constraints WHERE table_name IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove');
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','private_memory_migration_constraints','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','app_config','started','affected','rows',NULL,NULL,NULL,NULL);


DELETE FROM app_config WHERE key IN (
 'DREAMING_ENABLED','DREAMING_FORCE_ENABLED','DREAMING_START_TIME_LOCAL','DREAMING_REFLECT_ENABLED','DREAMING_REEVALUATE_ENABLED','DREAMING_DREAM_ENABLED','DREAMING_MAX_OUTPUTS',
 'COMMUNITY_DETECTION_ENABLED','COMMUNITY_DETECTION_START_TIME_LOCAL','COMMUNITY_DETECTION_MAX_CONCURRENCY','COMMUNITY_DETECTION_JITTER_SECONDS');
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','app_config','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','audit_log','started','affected','rows',NULL,NULL,NULL,NULL);


-- Remove graph-only history; leave security and Evidence audit records byte-for-byte intact.
DELETE FROM audit_log WHERE entity_type IN ('relationship','entity','value','predicate','hypothesis','dream','dream_cycle_run','dream_path_evaluation','community','community_run','relationship_conflict');
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','audit_log','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','operation_logs','started','affected','rows',NULL,NULL,NULL,NULL);

DELETE FROM operation_logs WHERE attrs->>'component' IN ('graph','dream','community','conflict_review','conflict_queue');
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('payload_cleanup','operation_logs','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;

-- Remove trigger/function dependencies before explicit RESTRICT table drops; unexpected retained dependencies block the transaction.

DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('table_retirement','triggers','started','completed','dependencies',NULL,NULL,NULL,NULL); END $progress$;
DO $retire$
DECLARE guard record; removed_constraint record; item record; removed_function record;
BEGIN
 FOR guard IN SELECT table_row.relname,tgname FROM pg_trigger trigger_row JOIN pg_class table_row ON table_row.oid=trigger_row.tgrelid
 JOIN pg_proc function_row ON function_row.oid=trigger_row.tgfoid WHERE table_row.relnamespace='public'::regnamespace AND NOT tgisinternal
 AND function_row.proname=ANY(ARRAY['hypotheses_guard_provenance','community_record_search_vector','submission_relationship_result_shape_valid','dense_mem_relationship_support_evidence_owner_defaults','dense_mem_relationship_support_occurrence_defaults','dense_mem_touch_community_topic_dependency','dense_mem_community_predicate_membership_dirty','dense_mem_community_topic_assignment_dirty','dense_mem_community_evidence_membership_dirty','dense_mem_community_topic_override_dirty','dense_mem_community_topic_current','dense_mem_community_dirty_entity_records','dense_mem_community_dirty_entity_names','dense_mem_community_dirty_evidence_fragments','dense_mem_community_dirty_knowledge_ingests','dense_mem_community_dirty_evidence_sources','dense_mem_community_dirty_evidence_occurrences','dense_mem_community_dirty_evidence_quarantines','dense_mem_community_dirty_evidence_lifecycle_events','dense_mem_community_dirty_relationship_records','dense_mem_community_dirty_relationship_evidence_supports','dense_mem_community_dirty_relationship_support_decision_events','dense_mem_community_dirty_team_predicate_definitions','dense_mem_community_dirty_ontology_record_heads','dense_mem_community_dirty_value_records','dense_mem_ontology_dirty_entity_records','dense_mem_ontology_dirty_entity_names','dense_mem_ontology_dirty_relationship_records','dense_mem_ontology_dirty_relationship_evidence_supports','dense_mem_ontology_dirty_relationship_support_decision_events','dense_mem_ontology_dirty_team_predicate_definitions']::text[]) LOOP
  EXECUTE format('DROP TRIGGER %I ON %I',guard.tgname,guard.relname);
  DELETE FROM evidence_only_triggers WHERE table_name=guard.relname AND trigger_name=guard.tgname;
 END LOOP;
 PERFORM pg_temp.evidence_only_progress('table_retirement','triggers','completed','completed','dependencies');
 PERFORM pg_temp.evidence_only_progress('table_retirement','constraints','started','completed','dependencies');
 FOR removed_constraint IN SELECT child.relname,constraint_row.conname FROM pg_constraint constraint_row JOIN pg_class child ON child.oid=constraint_row.conrelid
 JOIN pg_class parent ON parent.oid=constraint_row.confrelid WHERE constraint_row.contype='f'
 AND child.relnamespace='public'::regnamespace AND parent.relnamespace='public'::regnamespace
 AND child.relname IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove') AND parent.relname IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove') LOOP
  EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I',removed_constraint.relname,removed_constraint.conname);
 END LOOP;
 PERFORM pg_temp.evidence_only_progress('table_retirement','constraints','completed','completed','dependencies');
 DROP VIEW IF EXISTS semantic_edges;
 FOR item IN SELECT name FROM evidence_only_inventory WHERE disposition='remove' ORDER BY name LOOP
 PERFORM pg_temp.evidence_only_progress('table_retirement',item.name,'started','completed','tables',NULL,NULL,NULL,NULL);
 EXECUTE format('DROP TABLE IF EXISTS %I RESTRICT',item.name);
 PERFORM pg_temp.evidence_only_progress('table_retirement',item.name,'completed','completed','tables',1,1,NULL,NULL);
 END LOOP;
 PERFORM pg_temp.evidence_only_progress('table_retirement','functions','started','completed','dependencies');
 FOR removed_function IN SELECT oid::regprocedure::text AS signature FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname=ANY(ARRAY['hypotheses_guard_provenance','community_record_search_vector','submission_relationship_result_shape_valid','dense_mem_relationship_support_evidence_owner_defaults','dense_mem_relationship_support_occurrence_defaults','dense_mem_touch_community_topic_dependency','dense_mem_community_predicate_membership_dirty','dense_mem_community_topic_assignment_dirty','dense_mem_community_evidence_membership_dirty','dense_mem_community_topic_override_dirty','dense_mem_community_topic_current','dense_mem_community_dirty_entity_records','dense_mem_community_dirty_entity_names','dense_mem_community_dirty_evidence_fragments','dense_mem_community_dirty_knowledge_ingests','dense_mem_community_dirty_evidence_sources','dense_mem_community_dirty_evidence_occurrences','dense_mem_community_dirty_evidence_quarantines','dense_mem_community_dirty_evidence_lifecycle_events','dense_mem_community_dirty_relationship_records','dense_mem_community_dirty_relationship_evidence_supports','dense_mem_community_dirty_relationship_support_decision_events','dense_mem_community_dirty_team_predicate_definitions','dense_mem_community_dirty_ontology_record_heads','dense_mem_community_dirty_value_records','dense_mem_ontology_dirty_entity_records','dense_mem_ontology_dirty_entity_names','dense_mem_ontology_dirty_relationship_records','dense_mem_ontology_dirty_relationship_evidence_supports','dense_mem_ontology_dirty_relationship_support_decision_events','dense_mem_ontology_dirty_team_predicate_definitions']::text[]) LOOP
  EXECUTE 'DROP FUNCTION '||removed_function.signature||' RESTRICT';
 END LOOP;
 PERFORM pg_temp.evidence_only_progress('table_retirement','functions','completed','completed','dependencies');
END $retire$;

DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('guard_restoration','functions','started','completed','dependencies',NULL,NULL,NULL,NULL); END $progress$;


-- Defer the source/revision cycle only within transactions that explicitly request it.
ALTER TABLE evidence_sources DROP CONSTRAINT evidence_sources_current_revision_fk;
ALTER TABLE evidence_sources ADD CONSTRAINT evidence_sources_current_revision_fk
 FOREIGN KEY (team_id,source_id,current_revision_id,owner_profile_id)
 REFERENCES evidence_source_revisions(team_id,source_id,source_revision_id,owner_profile_id)
 ON DELETE NO ACTION DEFERRABLE INITIALLY IMMEDIATE;

CREATE OR REPLACE FUNCTION prevent_append_only_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'remember_failure_artifacts' THEN
        IF current_setting('app.tx_mode', true) = 'system'
           AND NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid IS NOT NULL
           AND COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) =
               (current_setting('app.remember_failure_artifact_retention_value', true) = 'true')
           AND (to_jsonb(NEW) - ARRAY['retained_by_legal_hold']) = (to_jsonb(OLD) - ARRAY['retained_by_legal_hold'])
           AND EXISTS (
               SELECT 1
               FROM remember_attempts AS attempt
               WHERE attempt.team_id = NEW.team_id
                 AND attempt.attempt_id = NEW.attempt_id
                 AND attempt.owner_profile_id = NEW.owner_profile_id
                 AND attempt.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
           )
           AND (
               (COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) AND EXISTS (
                   SELECT 1
                   FROM private_memory_legal_holds AS hold
                   WHERE hold.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
                     AND hold.released_at IS NULL
               ))
               OR
               (NOT COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) AND NOT EXISTS (
                   SELECT 1
                   FROM private_memory_legal_holds AS hold
                   WHERE hold.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
                     AND hold.released_at IS NULL
               ))
           ) THEN
            RETURN NEW;
        END IF;
    END IF;
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND (
           NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               = NULLIF(to_jsonb(OLD)->>'space_id', '')::uuid
           OR (
               TG_TABLE_NAME IN ('remember_attempt_events', 'remember_failure_artifacts', 'semantic_assessments')
               AND EXISTS (
                   SELECT 1
                   FROM remember_attempts AS attempt
                   WHERE attempt.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND attempt.attempt_id = NULLIF(to_jsonb(OLD)->>'attempt_id', '')::uuid
                     AND attempt.owner_profile_id = NULLIF(to_jsonb(OLD)->>'owner_profile_id', '')::uuid
                     AND attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               )
           )
           OR (
               TG_TABLE_NAME = 'remember_failure_artifacts'
               AND current_setting('app.remember_failure_artifact_purge', true) = 'true'
           )
           OR (
               TG_TABLE_NAME = 'remember_failure_artifacts'
               AND pg_trigger_depth() > 1
               AND NULLIF(current_setting('app.private_erasure_space_id', true), '') IS NOT NULL
           )
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION dense_mem_resolve_memory_handle(p_team UUID,p_table TEXT,p_id UUID,p_space UUID DEFAULT NULL) RETURNS UUID
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE previous_mode TEXT; allowed_spaces UUID[]; owner UUID; destination UUID;
BEGIN
 previous_mode:=current_setting('app.tx_mode',true);
 IF p_table <> 'evidence_fragments' OR
  previous_mode NOT IN ('team','profile') OR
  p_team IS DISTINCT FROM NULLIF(current_setting('app.current_team_id',true),'')::uuid THEN RETURN p_id; END IF;
 owner:=NULLIF(current_setting('app.memory_owner_id',true),'')::uuid;
 allowed_spaces:=string_to_array(NULLIF(current_setting('app.allowed_space_ids',true),''),',')::uuid[];
 PERFORM set_config('app.tx_mode','system',true);
 SELECT (mapping.destination_key->>1)::uuid INTO destination
 FROM private_memory_migration_records mapping
 JOIN memory_spaces space ON space.team_id=mapping.team_id AND space.id=mapping.destination_space_id
  AND space.generation=mapping.destination_generation AND space.lifecycle_state='active' AND space.kind='private'
 JOIN team_memberships membership ON membership.team_id=space.team_id AND membership.id=space.owner_membership_id AND membership.status='active'
 JOIN actor_identities actor ON actor.id=membership.actor_identity_id AND actor.active
 JOIN ownership_aliases alias ON alias.team_id=membership.team_id AND alias.canonical_identity_id=membership.actor_identity_id AND alias.legacy_owner_id=owner
 WHERE mapping.team_id=p_team AND mapping.source_table=p_table AND mapping.source_key->>1=p_id::text
  AND mapping.destination_membership_id=membership.id AND mapping.destination_space_id=ANY(allowed_spaces)
  AND (p_space IS NULL OR p_space=mapping.destination_space_id)
  AND mapping.completed AND NOT mapping.sealed AND mapping.purged_at IS NULL;
 PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true);
 RETURN COALESCE(destination,p_id);
EXCEPTION WHEN OTHERS THEN PERFORM set_config('app.tx_mode',COALESCE(previous_mode,''),true); RAISE;
END $$;

CREATE OR REPLACE FUNCTION dense_mem_ontology_dirty_ontology_record_heads() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE row_value ontology_record_heads%ROWTYPE;
BEGIN
 IF TG_OP='UPDATE' AND NEW.version=OLD.version THEN RETURN NEW; END IF;
 IF TG_OP='DELETE' THEN row_value:=OLD; ELSE row_value:=NEW; END IF;
 IF row_value.kind NOT IN ('topic','override') THEN RETURN row_value; END IF;
 PERFORM dense_mem_enqueue_ontology_marker(row_value.team_id,row_value.shared_space_id,row_value.space_generation,'ontology_record_heads',row_value.record_id::text,'definition',row_value.record_id::text);
 RETURN row_value;
END $$;


DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('guard_restoration','functions','completed','completed','dependencies',NULL,NULL,NULL,NULL); END $progress$;
-- EVIDENCE_ONLY_VERIFY_EVIDENCE

DO $verify$
DECLARE item record; progress_rows bigint; verified_total bigint:=0;
BEGIN
 FOR item IN SELECT name FROM evidence_only_inventory WHERE disposition='reset' LOOP
  EXECUTE format('SELECT count(*) FROM %I',item.name) INTO progress_rows;
  IF progress_rows<>0 THEN RAISE EXCEPTION 'evidence_only_migration_failed: ontology reset incomplete'; END IF;
 END LOOP;
 FOR item IN SELECT * FROM evidence_only_inventory WHERE disposition='retain' LOOP
  PERFORM pg_temp.evidence_only_progress('fingerprint_after',item.name,'started','affected','rows',NULL,item.row_count,NULL,NULL);
  EXECUTE format('INSERT INTO evidence_only_after SELECT %L,encode(sha256(convert_to(to_jsonb(row_value)::text,''UTF8'')),''hex'') FROM %I row_value',item.name,item.name);
  GET DIAGNOSTICS progress_rows=ROW_COUNT;
  verified_total:=verified_total+progress_rows;
  PERFORM pg_temp.evidence_only_progress('fingerprint_after',item.name,'completed','affected','rows',progress_rows,item.row_count,NULL,NULL);
 END LOOP;
 PERFORM pg_temp.evidence_only_progress('validation','retained_evidence','started','verified','fingerprints',0,verified_total,NULL,NULL);
 IF EXISTS((SELECT * FROM evidence_only_fingerprints EXCEPT SELECT * FROM evidence_only_after) UNION ALL (SELECT * FROM evidence_only_after EXCEPT SELECT * FROM evidence_only_fingerprints)) THEN
  RAISE EXCEPTION 'evidence_only_migration_blocked: retained fingerprint mismatch';
 END IF;
 IF EXISTS(SELECT 1 FROM evidence_only_inventory WHERE disposition='remove' AND to_regclass('public.'||name) IS NOT NULL) THEN RAISE EXCEPTION 'evidence_only_migration_failed: graph catalog remains'; END IF;
 FOR item IN SELECT * FROM evidence_only_triggers LOOP EXECUTE format('ALTER TABLE %I ENABLE TRIGGER %I',item.table_name,item.trigger_name); END LOOP;
 PERFORM pg_temp.evidence_only_progress('validation','retained_evidence','completed','verified','fingerprints',verified_total,verified_total,NULL,NULL);
END $verify$;
CREATE POLICY evidence_only_marker_insert ON v2_compatibility_markers FOR INSERT WITH CHECK (
 current_setting('app.tx_mode',true)='migration' AND marker_kind='evidence_only'
 AND version='dense-mem.v2.8.0' AND status='compatible');
DO $progress_work$ DECLARE progress_rows bigint; BEGIN
 PERFORM pg_temp.evidence_only_progress('receipt','v2_compatibility_markers','started','affected','rows',NULL,NULL,NULL,NULL);

INSERT INTO v2_compatibility_markers(marker_kind,version,status,metadata) VALUES('evidence_only','dense-mem.v2.8.0','compatible',jsonb_build_object(
 'inventory_sha256','b0d41120350071869ff9c3d77c6ddbb562341c41c06422f173b30cb4958df754', 'retained_counts',(SELECT jsonb_object_agg(name,row_count) FROM evidence_only_inventory WHERE disposition='retain'),
 'removed_counts',(SELECT jsonb_object_agg(name,row_count) FROM evidence_only_inventory WHERE disposition='remove'),
 'retained_fingerprint_sha256',(SELECT jsonb_object_agg(name,hash) FROM (SELECT inventory.name,encode(sha256(convert_to(COALESCE(string_agg(fingerprint,',' ORDER BY fingerprint),''),'UTF8')),'hex') AS hash FROM evidence_only_inventory inventory LEFT JOIN evidence_only_after hashes ON hashes.table_name=inventory.name WHERE disposition='retain' GROUP BY inventory.name) summaries),
 'retained_fingerprints_verified',true,'legacy_operation_keys',(SELECT count(*) FROM knowledge_ingests WHERE idempotency_key<>''),
 'reset_ontology_counts',(SELECT jsonb_object_agg(name,row_count) FROM evidence_only_inventory WHERE disposition='reset'),
 'resumable_sessions',(SELECT count(*) FROM evidence_only_sessions WHERE resumable),'transaction_outcome','committed'));
 GET DIAGNOSTICS progress_rows=ROW_COUNT;
 PERFORM pg_temp.evidence_only_progress('receipt','v2_compatibility_markers','completed','affected','rows',progress_rows,NULL,NULL,NULL);
END $progress_work$;
DROP POLICY evidence_only_marker_insert ON v2_compatibility_markers;

DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('policy_restoration','policies','started','completed','dependencies',NULL,NULL,NULL,NULL); END $progress$;
DO $restore_policies$
DECLARE item record;
BEGIN
 FOR item IN SELECT name FROM evidence_only_inventory WHERE disposition='mixed' LOOP EXECUTE format('DROP POLICY evidence_only_migration_repair ON %I',item.name); END LOOP;
 FOR item IN SELECT name FROM evidence_only_inventory WHERE disposition<>'remove' LOOP EXECUTE format('DROP POLICY evidence_only_inventory_read ON %I',item.name); END LOOP;
END $restore_policies$;
DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('policy_restoration','policies','completed','completed','dependencies',NULL,NULL,NULL,NULL); END $progress$;

DO $progress$ BEGIN PERFORM pg_temp.evidence_only_progress('sql_stream','migration','completed','completed','batches',1,1,NULL,NULL); END $progress$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Evidence-only removal is irreversible after commit; restore the matching verified backup, or recover forward after new writes'; END $$;
-- +goose StatementEnd
