-- Lock/rewrite impact: stopped-service transaction locks the explicit inventory; five-second acquisition bound, no provider calls.
-- RLS impact: temporary migration policies are removed before commit; FORCE RLS and runtime immutability remain enabled.
-- Backfill: retire mixed graph payloads and unfinished extraction checkpoints while retaining original Evidence and operation identity.
-- Preconditions: stop incompatible instances and verify a matching restore-tested backup before normal startup.
-- Recovery: before commit the transaction rolls back; after commit restore that backup, and after new writes recover forward unless separately approved.
-- Inventory identity: sha256:4d4269d72e53f44fa47476c7cf2ed658fa270e65339256c73b2f73faabe494d2
-- +goose Up
-- +goose StatementBegin
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);
SELECT set_config('app.current_team_id','',true);
SELECT set_config('app.current_profile_id','',true);
LOCK TABLE private_memory_legal_holds, memory_spaces IN SHARE ROW EXCLUSIVE MODE;
CREATE TEMP TABLE evidence_only_inventory(name text PRIMARY KEY, disposition text NOT NULL, row_count bigint NOT NULL DEFAULT 0) ON COMMIT DROP;
INSERT INTO evidence_only_inventory SELECT unnest(ARRAY['community_memberships','community_records','community_snapshot_runs','community_sources','community_summary_attempts','community_topic_dependencies','community_topic_discovery','community_topic_versions','community_topic_work','dream_cycle_runs','dream_diagnostic_captures','dream_evidence_target_attempts','dream_evidence_target_evaluations','dream_path_evaluation_run_links','dream_path_evaluations','entity_correction_events','entity_correction_plans','entity_names','entity_records','entity_resolution_events','hypotheses','hypothesis_derivation_sources','hypothesis_evidence_derivation_sources','hypothesis_feedback_events','predicate_definitions','predicate_registration_events','relationship_conflict_ai_assessment_attempts','relationship_conflict_ai_assessment_events','relationship_conflict_cases','relationship_conflict_derived_evidence_tasks','relationship_conflict_events','relationship_conflict_evidence_derivations','relationship_conflict_position_members','relationship_conflict_positions','relationship_conflict_resolution_plans','relationship_conflict_review_runs','relationship_correction_events','relationship_correction_submissions','relationship_cross_references','relationship_evidence_supports','relationship_observations','relationship_records','relationship_support_decision_events','relationship_transition_events','submission_relationship_results','team_predicate_definitions','value_records','verification_events','review_tasks','placement_assessments','placement_items','placement_outcomes','placement_runs']::text[]),'remove';
INSERT INTO evidence_only_inventory SELECT unnest(ARRAY['evidence_sources','evidence_source_revisions','evidence_exact_aliases','evidence_occurrences','evidence_fragments','evidence_security_events','evidence_security_signals','evidence_quarantines','evidence_lifecycle_operations','evidence_lifecycle_events','evidence_conflict_cases','evidence_conflict_positions','evidence_conflict_events']::text[]),'retain';
INSERT INTO evidence_only_inventory SELECT unnest(ARRAY['knowledge_ingests','remember_attempts','remember_attempt_events','remember_attempt_diagnostics','remember_failure_artifacts','remember_invocation_diagnostics','semantic_assessments','session_submissions','session_events','session_extraction_checkpoints','session_submission_receipts','session_submission_diagnostics','ontology_record_heads','ontology_record_revisions','ontology_source_dependencies','ontology_revision_dependencies','ontology_publications','ontology_assessments','ontology_draft_heads','ontology_draft_revisions','ontology_maintenance_sources','ontology_maintenance_markers','ontology_maintenance_teams','ontology_maintenance_batches','recall_feedback_events','audit_log','operation_logs','search_documents','search_projection_generations','private_memory_migration_records','private_memory_migration_constraints','app_config']::text[]),'mixed';
CREATE TEMP TABLE evidence_only_triggers(table_name text, trigger_name text) ON COMMIT DROP;
CREATE TEMP TABLE evidence_only_fingerprints(table_name text, fingerprint text, PRIMARY KEY(table_name,fingerprint)) ON COMMIT DROP;
CREATE TEMP TABLE evidence_only_after(LIKE evidence_only_fingerprints INCLUDING ALL) ON COMMIT DROP;

DO $inventory$
DECLARE item record; held bigint; unknown_dependencies text;
BEGIN
 FOR item IN SELECT * FROM evidence_only_inventory ORDER BY name LOOP
  IF to_regclass('public.'||item.name) IS NULL THEN
   IF item.disposition<>'remove' THEN RAISE EXCEPTION 'evidence_only_migration_blocked: missing inventory table %',item.name; END IF;
   CONTINUE;
  END IF;
  EXECUTE format('LOCK TABLE %I IN ACCESS EXCLUSIVE MODE',item.name);
  EXECUTE format('CREATE POLICY evidence_only_inventory_read ON %I FOR SELECT USING(current_setting(''app.tx_mode'',true)=''migration'')',item.name);
 END LOOP;
 FOR item IN SELECT * FROM evidence_only_inventory WHERE to_regclass('public.'||name) IS NOT NULL ORDER BY name LOOP
  EXECUTE format('SELECT count(*) FROM %I',item.name) INTO held;
  UPDATE evidence_only_inventory SET row_count=held WHERE name=item.name;
  IF item.disposition='retain' THEN
   EXECUTE format('INSERT INTO evidence_only_fingerprints SELECT %L,encode(sha256(convert_to(to_jsonb(row_value)::text,''UTF8'')),''hex'') FROM %I row_value',item.name,item.name);
  ELSE
   EXECUTE format($held$SELECT count(*) FROM %I row_value WHERE
    COALESCE((to_jsonb(row_value)->>'retained_by_legal_hold')::boolean,false)
    OR dense_mem_private_space_held(NULLIF(to_jsonb(row_value)->>'space_id','')::uuid)
    OR dense_mem_private_space_held(NULLIF(to_jsonb(row_value)->>'memory_space_id','')::uuid)
    OR EXISTS(SELECT 1 FROM remember_attempts attempt
     WHERE attempt.team_id=NULLIF(to_jsonb(row_value)->>'team_id','')::uuid
      AND attempt.attempt_id=NULLIF(to_jsonb(row_value)->>'attempt_id','')::uuid
      AND dense_mem_private_space_held(attempt.space_id))$held$,item.name) INTO held;
   IF held>0 THEN RAISE EXCEPTION 'evidence_only_migration_blocked: legal hold table=% rows=%',item.name,held; END IF;
  END IF;
 END LOOP;
 SELECT string_agg(child.relname||'.'||constraint_row.conname,', ' ORDER BY child.relname,constraint_row.conname)
 INTO unknown_dependencies FROM pg_constraint constraint_row
 JOIN pg_class child ON child.oid=constraint_row.conrelid
 JOIN pg_class parent ON parent.oid=constraint_row.confrelid
 WHERE constraint_row.contype='f' AND parent.relnamespace='public'::regnamespace AND child.relnamespace='public'::regnamespace
 AND parent.relname IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove')
 AND child.relname NOT IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove');
 IF unknown_dependencies IS NOT NULL THEN RAISE EXCEPTION 'evidence_only_migration_blocked: unhandled retained dependency %',unknown_dependencies; END IF;
END $inventory$;

-- Only inventory-owned guards and policies are opened, inside this transaction; their definitions are restored below.
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

UPDATE knowledge_ingests SET metadata=metadata || jsonb_build_object('retired_payload',true,'retirement_reason','factual_graph_removed','original_status',status)
 WHERE COALESCE(metadata->>'contract_version','')<>'dense-mem.v2.8.0';
ALTER TABLE knowledge_ingests DROP COLUMN proposal;
UPDATE remember_attempts SET public_result=pg_temp.evidence_only_result(public_result);
ALTER TABLE remember_attempts DROP CONSTRAINT remember_attempts_counts_check;
ALTER TABLE remember_attempts DROP COLUMN relationship_count;
ALTER TABLE remember_attempts ADD CONSTRAINT remember_attempts_counts_check CHECK(evidence_count>=0 AND document_count>=0 AND assessor_turns>=0 AND duration_ms>=0);
-- The original submission kind remains accounting history, never an executable contract.
UPDATE remember_attempt_events SET metadata=pg_temp.evidence_only_result(metadata);
UPDATE semantic_assessments SET response_history=COALESCE((SELECT jsonb_agg(
 CASE WHEN entry ? 'normalized_response' THEN jsonb_set(entry,'{normalized_response}',pg_temp.evidence_only_assessment(entry->'normalized_response')) ELSE pg_temp.evidence_only_assessment(entry) END ORDER BY ordinal)
 FROM jsonb_array_elements(response_history) WITH ORDINALITY AS entries(entry,ordinal)),'[]'::jsonb);
UPDATE remember_attempt_diagnostics SET
 request_bytes=CASE WHEN kind='original_request' THEN pg_temp.evidence_only_request(request_bytes) ELSE convert_to('{"retired_payload":true,"availability":"legacy_provider_payload_unavailable"}','UTF8') END,
 response_bytes=CASE WHEN kind='caller_response' THEN pg_temp.evidence_only_request(response_bytes) ELSE convert_to('{"retired_payload":true,"availability":"legacy_provider_payload_unavailable"}','UTF8') END,
 capture_state='not_captured';
UPDATE remember_invocation_diagnostics SET request_bytes=pg_temp.evidence_only_request(request_bytes),response_bytes=pg_temp.evidence_only_request(response_bytes),
 request_capture_state='unavailable',response_capture_state='unavailable',request_capture_reason='legacy_graph_payload_retired',response_capture_reason='legacy_graph_payload_retired',
 provider_exchanges=COALESCE((SELECT jsonb_agg((exchange-ARRAY['request_body','response_body','request_bytes','response_bytes','RequestBody','ResponseBody','ResponseBodyProjection'])||jsonb_build_object('retired_payload',true,'availability','legacy_provider_payload_unavailable') ORDER BY ordinal)
 FROM jsonb_array_elements(provider_exchanges) WITH ORDINALITY entries(exchange,ordinal)),'[]'::jsonb);
UPDATE remember_failure_artifacts SET content_bytes=convert_to('{"retired_payload":true,"availability":"legacy_failure_payload_unavailable"}','UTF8'),
 byte_count=octet_length(convert_to('{"retired_payload":true,"availability":"legacy_failure_payload_unavailable"}','UTF8')),
 content_sha256='sha256:'||encode(sha256(convert_to('{"retired_payload":true,"availability":"legacy_failure_payload_unavailable"}','UTF8')),'hex');

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
DELETE FROM session_extraction_checkpoints checkpoint USING evidence_only_sessions legacy
 WHERE checkpoint.team_id=legacy.team_id AND checkpoint.space_id=legacy.space_id AND checkpoint.submission_id=legacy.submission_id
 AND (legacy.resumable OR checkpoint.window_index=-1);
UPDATE session_extraction_checkpoints SET body=pg_temp.evidence_only_assessment(body)-ARRAY['entities','relationships','groups']||jsonb_build_object('availability','retired_contract');
UPDATE session_submissions submission SET intake=CASE WHEN legacy.resumable THEN jsonb_set(intake,'{ExtractionVersion}','"session_evidence_excerpts_v2"'::jsonb)
 ||jsonb_build_object('EvidenceOnlyResume',jsonb_build_object('original_version',legacy.original_version,'original_outcome',legacy.original_outcome,'cleared_checkpoints',legacy.cleared_checkpoints)) ELSE intake END,
 result=CASE WHEN legacy.resumable THEN NULL ELSE pg_temp.evidence_only_session_result(result) END
 FROM evidence_only_sessions legacy WHERE submission.team_id=legacy.team_id AND submission.space_id=legacy.space_id AND submission.submission_id=legacy.submission_id;
UPDATE session_submission_receipts SET body=pg_temp.evidence_only_session_result(body);
UPDATE session_submission_diagnostics SET body=jsonb_build_object('capture_state','unavailable','retired_payload',true,'retirement_reason','factual_graph_removed',
 'payload',jsonb_build_object('tool',body->'payload'->'tool','request',body->'payload'->'request','result',pg_temp.evidence_only_result(body->'payload'->'result'),
 'exchanges',COALESCE((SELECT jsonb_agg((exchange-ARRAY['RequestBody','ResponseBody','ResponseBodyProjection'])||jsonb_build_object('retired_payload',true,'availability','legacy_provider_payload_unavailable') ORDER BY ordinal)
 FROM jsonb_array_elements(COALESCE(body->'payload'->'exchanges','[]'::jsonb)) WITH ORDINALITY entries(exchange,ordinal)),'[]'::jsonb)));
ALTER TABLE session_extraction_checkpoints DROP CONSTRAINT session_extraction_checkpoints_window_index_check;
ALTER TABLE session_extraction_checkpoints ADD CONSTRAINT session_extraction_checkpoints_window_index_check CHECK(window_index BETWEEN 0 AND 7);

-- Delete obsolete semantic ontology rows, then their reverse dependencies, without touching topic definitions or Evidence membership overrides.
CREATE TEMP TABLE evidence_only_retired_records ON COMMIT DROP AS
 SELECT DISTINCT team_id,shared_space_id,space_generation,record_id FROM ontology_record_revisions revision
 WHERE kind NOT IN ('topic','assignment','evidence_group','override')

 OR (kind='assignment' AND body->'assignment'->'source'->>'kind'<>'evidence')
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(body->'group'->'members',body->'override'->'members','[]'::jsonb)) member WHERE member->>'kind'<>'evidence');
DO $closure$
DECLARE changed bigint;
BEGIN LOOP
 INSERT INTO evidence_only_retired_records SELECT DISTINCT dependency.team_id,dependency.shared_space_id,dependency.space_generation,dependency.record_id
 FROM ontology_record_revisions dependency JOIN evidence_only_retired_records retired
 ON (dependency.team_id,dependency.shared_space_id,dependency.space_generation)=(retired.team_id,retired.shared_space_id,retired.space_generation)
 AND retired.record_id::text IN (dependency.body->'assignment'->>'definition_id',dependency.body->'override'->>'definition_id',dependency.body->'override'->>'target_id')
 WHERE NOT EXISTS(SELECT 1 FROM evidence_only_retired_records present WHERE (dependency.team_id,dependency.shared_space_id,dependency.space_generation,dependency.record_id)=(present.team_id,present.shared_space_id,present.space_generation,present.record_id));
 GET DIAGNOSTICS changed=ROW_COUNT; EXIT WHEN changed=0;
END LOOP; END $closure$;
DELETE FROM ontology_revision_dependencies dependency WHERE EXISTS(SELECT 1 FROM evidence_only_retired_records retired WHERE
 (dependency.team_id,dependency.shared_space_id,dependency.space_generation)=(retired.team_id,retired.shared_space_id,retired.space_generation) AND (dependency.record_id=retired.record_id OR dependency.dependency_id=retired.record_id));
DELETE FROM ontology_source_dependencies dependency WHERE source_kind<>'evidence' OR EXISTS(SELECT 1 FROM evidence_only_retired_records retired WHERE
 (dependency.team_id,dependency.shared_space_id,dependency.space_generation,dependency.record_id)=(retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id));
DELETE FROM ontology_record_heads head USING evidence_only_retired_records retired WHERE
 (head.team_id,head.shared_space_id,head.space_generation,head.record_id)=(retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id);
DELETE FROM ontology_record_revisions revision USING evidence_only_retired_records retired WHERE
 (revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id)=(retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id);
CREATE TEMP TABLE evidence_only_rebind_records ON COMMIT DROP AS
 SELECT revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id,revision.version,revision.fingerprint AS original_fingerprint
 FROM ontology_record_revisions revision WHERE
 EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(body->'sources','[]'::jsonb)) source WHERE source->>'kind'<>'evidence')
 OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(body->'dependencies','[]'::jsonb)) dependency JOIN evidence_only_retired_records retired
  ON (revision.team_id,revision.shared_space_id,revision.space_generation,dependency->>'id')=(retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id::text));
UPDATE ontology_record_revisions revision SET body=jsonb_set(jsonb_set(body,'{sources}',
 COALESCE((SELECT jsonb_agg(source ORDER BY ordinal) FROM jsonb_array_elements(COALESCE(body->'sources','[]'::jsonb)) WITH ORDINALITY entries(source,ordinal) WHERE source->>'kind'='evidence'),'[]'::jsonb)),
 '{dependencies}',COALESCE((SELECT jsonb_agg(dependency ORDER BY ordinal) FROM jsonb_array_elements(COALESCE(body->'dependencies','[]'::jsonb)) WITH ORDINALITY entries(dependency,ordinal)
 WHERE NOT EXISTS(SELECT 1 FROM evidence_only_retired_records retired WHERE
  (revision.team_id,revision.shared_space_id,revision.space_generation,dependency->>'id')=(retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id::text))),'[]'::jsonb))
 WHERE EXISTS(SELECT 1 FROM evidence_only_rebind_records changed WHERE
 (revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id,revision.version)=(changed.team_id,changed.shared_space_id,changed.space_generation,changed.record_id,changed.version));
CREATE FUNCTION pg_temp.evidence_only_record_refs(p_team uuid,p_space uuid,p_generation bigint,refs jsonb) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT COALESCE(jsonb_agg(ref ORDER BY ordinal),'[]'::jsonb)
 FROM jsonb_array_elements(COALESCE(refs,'[]'::jsonb)) WITH ORDINALITY entries(ref,ordinal)
 WHERE NOT EXISTS(SELECT 1 FROM evidence_only_retired_records retired
 WHERE (retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id::text)=(p_team,p_space,p_generation,ref->>'id'));
$$;
UPDATE ontology_publications publication SET result=jsonb_set(result,'{records}',COALESCE((SELECT jsonb_agg(ref) FROM jsonb_array_elements(COALESCE(result->'records','[]'::jsonb)) ref
 WHERE NOT EXISTS(SELECT 1 FROM evidence_only_retired_records retired WHERE (publication.team_id,publication.shared_space_id,publication.space_generation,ref->>'id')=(retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id::text))),'[]'::jsonb))||jsonb_build_object('retired_payload',true);
CREATE FUNCTION pg_temp.evidence_only_draft(p_team uuid,p_space uuid,p_generation bigint,body jsonb) RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE kept jsonb; impacted boolean;
BEGIN
 SELECT COALESCE(jsonb_agg(edit ORDER BY ordinal),'[]'::jsonb) INTO kept
 FROM jsonb_array_elements(COALESCE(body->'draft'->'edits','[]'::jsonb)) WITH ORDINALITY entries(edit,ordinal)
 WHERE COALESCE(edit->>'kind','topic') IN ('topic','assignment','evidence_group','override')
 AND COALESCE(edit->'source'->>'kind','evidence')='evidence'
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(edit->'members','[]'::jsonb)) member WHERE member->>'kind'<>'evidence')
 AND NOT EXISTS(SELECT 1 FROM evidence_only_retired_records retired WHERE
  (retired.team_id,retired.shared_space_id,retired.space_generation)=(p_team,p_space,p_generation)
  AND retired.record_id::text IN (edit->>'record_id',edit->>'definition_id'));
 impacted:=jsonb_array_length(kept)<>jsonb_array_length(COALESCE(body->'draft'->'edits','[]'::jsonb)) OR EXISTS(
  SELECT 1 FROM jsonb_array_elements(COALESCE(body->'prepared'->'changes','[]'::jsonb)) change
  WHERE change->'record'->>'kind' NOT IN ('topic','assignment','evidence_group','override')
   OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(change->'record'->'sources','[]'::jsonb)) source WHERE source->>'kind'<>'evidence')
   OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(change->'record'->'dependencies','[]'::jsonb)) dependency JOIN evidence_only_retired_records retired
    ON (retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id::text)=(p_team,p_space,p_generation,dependency->>'id')));
 body:=jsonb_set(body,'{draft,edits}',kept);
 IF body->'draft' ? 'publication' THEN body:=jsonb_set(body,'{draft,publication,records}',pg_temp.evidence_only_record_refs(p_team,p_space,p_generation,body->'draft'->'publication'->'records')); END IF;
 IF impacted THEN
  body:=body||jsonb_build_object('retired_payload',true,'retirement_reason','factual_graph_removed','original_state',body->'draft'->>'state','prepared','{}'::jsonb);
  body:=jsonb_set(body,'{draft,state}','"discarded"'::jsonb);
 END IF;
 RETURN body;
END $$;
UPDATE ontology_draft_heads SET body=pg_temp.evidence_only_draft(team_id,shared_space_id,space_generation,body);
UPDATE ontology_draft_heads SET state=body->'draft'->>'state';
UPDATE ontology_draft_revisions SET body=pg_temp.evidence_only_draft(team_id,shared_space_id,space_generation,body);
UPDATE ontology_assessments assessment SET body=jsonb_set(jsonb_set(jsonb_set(body,'{sources}',
 COALESCE((SELECT jsonb_agg(source ORDER BY ordinal) FROM jsonb_array_elements(body->'sources') WITH ORDINALITY entries(source,ordinal) WHERE source->>'kind'='evidence'),'[]'::jsonb)),
 '{dependencies}',pg_temp.evidence_only_record_refs(team_id,shared_space_id,space_generation,body->'dependencies')),
 '{result}',((body->'result')-ARRAY['outcomes','publication','ambiguous_comparisons','freshness_rejection']) || jsonb_build_object(
 'outcomes',COALESCE((SELECT jsonb_agg(jsonb_set(outcome,'{record_ids}',COALESCE((SELECT jsonb_agg(id ORDER BY id_ordinal)
  FROM jsonb_array_elements(COALESCE(outcome->'record_ids','[]'::jsonb)) WITH ORDINALITY ids(id,id_ordinal)
  WHERE NOT EXISTS(SELECT 1 FROM evidence_only_retired_records retired
   WHERE (retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id::text)=(assessment.team_id,assessment.shared_space_id,assessment.space_generation,id#>>'{}'))),'[]'::jsonb)) ORDER BY ordinal)
  FROM jsonb_array_elements(body->'result'->'outcomes') WITH ORDINALITY entries(outcome,ordinal) WHERE outcome->'source'->>'kind'='evidence'),'[]'::jsonb),
 'ambiguous_comparisons',COALESCE((SELECT jsonb_agg(comparison ORDER BY ordinal) FROM jsonb_array_elements(COALESCE(body->'result'->'ambiguous_comparisons','[]'::jsonb)) WITH ORDINALITY entries(comparison,ordinal)
  WHERE comparison->'left'->>'kind'='evidence' AND comparison->'right'->>'kind'='evidence'),'[]'::jsonb))
 || CASE WHEN body->'result' ? 'publication' THEN jsonb_build_object('publication',jsonb_set(body->'result'->'publication','{records}',pg_temp.evidence_only_record_refs(team_id,shared_space_id,space_generation,body->'result'->'publication'->'records'))) ELSE '{}'::jsonb END
 || CASE WHEN body->'result' ? 'freshness_rejection' AND COALESCE(body->'result'->'freshness_rejection'->'source'->>'kind','evidence')='evidence'
  AND NOT EXISTS(SELECT 1 FROM evidence_only_retired_records retired WHERE (retired.team_id,retired.shared_space_id,retired.space_generation,retired.record_id::text)=(assessment.team_id,assessment.shared_space_id,assessment.space_generation,body->'result'->'freshness_rejection'->>'record_id'))
  THEN jsonb_build_object('freshness_rejection',jsonb_set((body->'result'->'freshness_rejection')-ARRAY['expected_context','observed_context'],'{dependencies}',pg_temp.evidence_only_record_refs(team_id,shared_space_id,space_generation,body->'result'->'freshness_rejection'->'dependencies'))) ELSE '{}'::jsonb END);
UPDATE ontology_assessments SET body=body||jsonb_build_object('retired_payload',true,'availability','legacy_organization_payload_retired') WHERE jsonb_array_length(body->'sources')=0;

DELETE FROM ontology_maintenance_sources WHERE source_kind<>'evidence';
UPDATE ontology_maintenance_sources SET status=CASE WHEN eligible THEN 'pending' ELSE 'unavailable' END,
 reason='evidence_only_contract',assessment_id=NULL,revision=revision+1,pending_at=clock_timestamp(),updated_at=clock_timestamp();
DELETE FROM ontology_maintenance_markers WHERE target_kind IN ('entity','predicate','relationship') OR anchor_kind IN ('entity_records','entity_names','relationship_records','relationship_evidence_supports','relationship_support_decision_events','team_predicate_definitions');
UPDATE ontology_maintenance_teams SET discovery_kind=0,discovery_cursor='',lease_token=NULL,lease_until=NULL;
ALTER TABLE ontology_maintenance_batches DROP CONSTRAINT ontology_maintenance_batches_sources_check;
UPDATE ontology_maintenance_batches SET sources=COALESCE((SELECT jsonb_agg(source) FROM jsonb_array_elements(sources) source WHERE source->>'kind'='evidence'),'[]'::jsonb),
 revisions=jsonb_build_object('retired_contract',true,'original_status',status),
 status=CASE WHEN status='running' THEN 'lost' ELSE status END,
 failure_code=CASE WHEN status='running' THEN 'retired_contract' ELSE failure_code END,
 completed_at=CASE WHEN status='running' THEN clock_timestamp() ELSE completed_at END;
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
UPDATE recall_feedback_events SET result_refs=pg_temp.evidence_only_feedback_refs(result_refs),
 tool_args=tool_args-ARRAY['use_communities','known_relationship_ids','relationship_ids'],
 snapshot_metadata=snapshot_metadata||jsonb_build_object('retired_payload',true);
UPDATE recall_feedback_events SET result_count=jsonb_array_length(result_refs),irrelevant_result_refs=pg_temp.evidence_only_feedback_refs(irrelevant_result_refs);
ALTER TABLE recall_feedback_events DROP COLUMN dream_feedback;
DELETE FROM search_documents WHERE source_kind<>'evidence';
DELETE FROM search_projection_generations WHERE source_kind<>'evidence';
ALTER TABLE search_documents DROP CONSTRAINT search_documents_source_kind_check;
ALTER TABLE search_documents ADD CONSTRAINT search_documents_source_kind_check CHECK(source_kind='evidence');
ALTER TABLE search_projection_generations DROP CONSTRAINT search_projection_generations_source_kind_check;
ALTER TABLE search_projection_generations ADD CONSTRAINT search_projection_generations_source_kind_check CHECK(source_kind='evidence');
DROP INDEX IF EXISTS search_documents_relationship_projection_idx;
DELETE FROM private_memory_migration_records WHERE source_table IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove');
DELETE FROM private_memory_migration_constraints WHERE table_name IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove');

DELETE FROM app_config WHERE key IN (
 'DREAMING_ENABLED','DREAMING_FORCE_ENABLED','DREAMING_START_TIME_LOCAL','DREAMING_REFLECT_ENABLED','DREAMING_REEVALUATE_ENABLED','DREAMING_DREAM_ENABLED','DREAMING_MAX_OUTPUTS',
 'COMMUNITY_DETECTION_ENABLED','COMMUNITY_DETECTION_START_TIME_LOCAL','COMMUNITY_DETECTION_MAX_CONCURRENCY','COMMUNITY_DETECTION_JITTER_SECONDS');

-- Remove graph-only history; leave security and Evidence audit records byte-for-byte intact.
DELETE FROM audit_log WHERE entity_type IN ('relationship','entity','value','predicate','hypothesis','dream','dream_cycle_run','dream_path_evaluation','community','community_run','relationship_conflict');
DELETE FROM operation_logs WHERE attrs->>'component' IN ('graph','dream','community','conflict_review','conflict_queue');

-- Remove trigger/function dependencies before explicit RESTRICT table drops; unexpected retained dependencies block the transaction.
DO $retire$
DECLARE guard record; removed_constraint record; item record; removed_function record;
BEGIN
 FOR guard IN SELECT table_row.relname,tgname FROM pg_trigger trigger_row JOIN pg_class table_row ON table_row.oid=trigger_row.tgrelid
 JOIN pg_proc function_row ON function_row.oid=trigger_row.tgfoid WHERE table_row.relnamespace='public'::regnamespace AND NOT tgisinternal
 AND function_row.proname=ANY(ARRAY['hypotheses_guard_provenance','community_record_search_vector','submission_relationship_result_shape_valid','dense_mem_relationship_support_evidence_owner_defaults','dense_mem_relationship_support_occurrence_defaults','dense_mem_touch_community_topic_dependency','dense_mem_community_predicate_membership_dirty','dense_mem_community_topic_assignment_dirty','dense_mem_community_evidence_membership_dirty','dense_mem_community_topic_override_dirty','dense_mem_community_topic_current','dense_mem_community_dirty_entity_records','dense_mem_community_dirty_entity_names','dense_mem_community_dirty_evidence_fragments','dense_mem_community_dirty_knowledge_ingests','dense_mem_community_dirty_evidence_sources','dense_mem_community_dirty_evidence_occurrences','dense_mem_community_dirty_evidence_quarantines','dense_mem_community_dirty_evidence_lifecycle_events','dense_mem_community_dirty_relationship_records','dense_mem_community_dirty_relationship_evidence_supports','dense_mem_community_dirty_relationship_support_decision_events','dense_mem_community_dirty_team_predicate_definitions','dense_mem_community_dirty_ontology_record_heads','dense_mem_community_dirty_value_records','dense_mem_ontology_dirty_entity_records','dense_mem_ontology_dirty_entity_names','dense_mem_ontology_dirty_relationship_records','dense_mem_ontology_dirty_relationship_evidence_supports','dense_mem_ontology_dirty_relationship_support_decision_events','dense_mem_ontology_dirty_team_predicate_definitions']::text[]) LOOP
  EXECUTE format('DROP TRIGGER %I ON %I',guard.tgname,guard.relname);
  DELETE FROM evidence_only_triggers WHERE table_name=guard.relname AND trigger_name=guard.tgname;
 END LOOP;
 FOR removed_constraint IN SELECT child.relname,constraint_row.conname FROM pg_constraint constraint_row JOIN pg_class child ON child.oid=constraint_row.conrelid
 JOIN pg_class parent ON parent.oid=constraint_row.confrelid WHERE constraint_row.contype='f'
 AND child.relnamespace='public'::regnamespace AND parent.relnamespace='public'::regnamespace
 AND child.relname IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove') AND parent.relname IN (SELECT name FROM evidence_only_inventory WHERE disposition='remove') LOOP
  EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I',removed_constraint.relname,removed_constraint.conname);
 END LOOP;
 DROP VIEW IF EXISTS semantic_edges;
 FOR item IN SELECT name FROM evidence_only_inventory WHERE disposition='remove' ORDER BY name LOOP EXECUTE format('DROP TABLE IF EXISTS %I RESTRICT',item.name); END LOOP;
 FOR removed_function IN SELECT oid::regprocedure::text AS signature FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname=ANY(ARRAY['hypotheses_guard_provenance','community_record_search_vector','submission_relationship_result_shape_valid','dense_mem_relationship_support_evidence_owner_defaults','dense_mem_relationship_support_occurrence_defaults','dense_mem_touch_community_topic_dependency','dense_mem_community_predicate_membership_dirty','dense_mem_community_topic_assignment_dirty','dense_mem_community_evidence_membership_dirty','dense_mem_community_topic_override_dirty','dense_mem_community_topic_current','dense_mem_community_dirty_entity_records','dense_mem_community_dirty_entity_names','dense_mem_community_dirty_evidence_fragments','dense_mem_community_dirty_knowledge_ingests','dense_mem_community_dirty_evidence_sources','dense_mem_community_dirty_evidence_occurrences','dense_mem_community_dirty_evidence_quarantines','dense_mem_community_dirty_evidence_lifecycle_events','dense_mem_community_dirty_relationship_records','dense_mem_community_dirty_relationship_evidence_supports','dense_mem_community_dirty_relationship_support_decision_events','dense_mem_community_dirty_team_predicate_definitions','dense_mem_community_dirty_ontology_record_heads','dense_mem_community_dirty_value_records','dense_mem_ontology_dirty_entity_records','dense_mem_ontology_dirty_entity_names','dense_mem_ontology_dirty_relationship_records','dense_mem_ontology_dirty_relationship_evidence_supports','dense_mem_ontology_dirty_relationship_support_decision_events','dense_mem_ontology_dirty_team_predicate_definitions']::text[]) LOOP
  EXECUTE 'DROP FUNCTION '||removed_function.signature||' RESTRICT';
 END LOOP;
END $retire$;

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

-- EVIDENCE_ONLY_REBIND_FINGERPRINTS

DO $verify$
DECLARE item record;
BEGIN
 IF current_setting('app.evidence_only_rebound',true) IS DISTINCT FROM 'true' THEN RAISE EXCEPTION 'evidence_only_migration_blocked: canonical metadata fingerprint phase required'; END IF;
 FOR item IN SELECT * FROM evidence_only_inventory WHERE disposition='retain' LOOP
  EXECUTE format('INSERT INTO evidence_only_after SELECT %L,encode(sha256(convert_to(to_jsonb(row_value)::text,''UTF8'')),''hex'') FROM %I row_value',item.name,item.name);
 END LOOP;
 IF EXISTS((SELECT * FROM evidence_only_fingerprints EXCEPT SELECT * FROM evidence_only_after) UNION ALL (SELECT * FROM evidence_only_after EXCEPT SELECT * FROM evidence_only_fingerprints)) THEN
  RAISE EXCEPTION 'evidence_only_migration_blocked: retained fingerprint mismatch';
 END IF;
 IF EXISTS(SELECT 1 FROM evidence_only_inventory WHERE disposition='remove' AND to_regclass('public.'||name) IS NOT NULL) THEN RAISE EXCEPTION 'evidence_only_migration_failed: graph catalog remains'; END IF;
 FOR item IN SELECT * FROM evidence_only_triggers LOOP EXECUTE format('ALTER TABLE %I ENABLE TRIGGER %I',item.table_name,item.trigger_name); END LOOP;
END $verify$;
CREATE POLICY evidence_only_marker_insert ON v2_compatibility_markers FOR INSERT WITH CHECK (
 current_setting('app.tx_mode',true)='migration' AND marker_kind='evidence_only'
 AND version='dense-mem.v2.8.0' AND status='compatible');
INSERT INTO v2_compatibility_markers(marker_kind,version,status,metadata) VALUES('evidence_only','dense-mem.v2.8.0','compatible',jsonb_build_object(
 'inventory_sha256','4d4269d72e53f44fa47476c7cf2ed658fa270e65339256c73b2f73faabe494d2', 'retained_counts',(SELECT jsonb_object_agg(name,row_count) FROM evidence_only_inventory WHERE disposition='retain'),
 'removed_counts',(SELECT jsonb_object_agg(name,row_count) FROM evidence_only_inventory WHERE disposition='remove'),
 'retained_fingerprint_sha256',(SELECT jsonb_object_agg(name,hash) FROM (SELECT inventory.name,encode(sha256(convert_to(COALESCE(string_agg(fingerprint,',' ORDER BY fingerprint),''),'UTF8')),'hex') AS hash FROM evidence_only_inventory inventory LEFT JOIN evidence_only_after hashes ON hashes.table_name=inventory.name WHERE disposition='retain' GROUP BY inventory.name) summaries),
 'retained_fingerprints_verified',true,'legacy_operation_keys',(SELECT count(*) FROM knowledge_ingests WHERE idempotency_key<>''),
 'rebound_ontology_records',(SELECT count(*) FROM evidence_only_rebind_records),
 'resumable_sessions',(SELECT count(*) FROM evidence_only_sessions WHERE resumable),'transaction_outcome','committed'));
DROP POLICY evidence_only_marker_insert ON v2_compatibility_markers;
DO $restore_policies$
DECLARE item record;
BEGIN
 FOR item IN SELECT name FROM evidence_only_inventory WHERE disposition='mixed' LOOP EXECUTE format('DROP POLICY evidence_only_migration_repair ON %I',item.name); END LOOP;
 FOR item IN SELECT name FROM evidence_only_inventory WHERE disposition<>'remove' LOOP EXECUTE format('DROP POLICY evidence_only_inventory_read ON %I',item.name); END LOOP;
END $restore_policies$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Evidence-only removal is irreversible after commit; restore the matching verified backup, or recover forward after new writes'; END $$;
-- +goose StatementEnd
