--
-- PostgreSQL database dump
--



SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: relationship_conflict_cases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_cases (
    team_id uuid NOT NULL,
    conflict_id uuid DEFAULT gen_random_uuid() NOT NULL,
    semantic_scope_key text NOT NULL,
    kind text DEFAULT 'cross_profile_current_state'::text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    subject_entity_id uuid NOT NULL,
    predicate_key text NOT NULL,
    predicate_version integer DEFAULT 1 NOT NULL,
    relationship_kind text NOT NULL,
    current_cardinality text NOT NULL,
    polarity text DEFAULT '+'::text NOT NULL,
    scope_key text,
    question text DEFAULT ''::text NOT NULL,
    policy_version text DEFAULT 'cross_profile_conflict_v1'::text NOT NULL,
    review_due_at timestamp with time zone NOT NULL,
    next_review_at timestamp with time zone NOT NULL,
    review_ttl_days integer NOT NULL,
    timezone text DEFAULT 'Local'::text NOT NULL,
    preferred_position_id uuid,
    resolved_at timestamp with time zone,
    effective_at timestamp with time zone,
    effective_time_basis text DEFAULT ''::text NOT NULL,
    resolution_reason text DEFAULT ''::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    lease_worker_id text DEFAULT ''::text NOT NULL,
    lease_until timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    last_review_run_id uuid,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_cases_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT relationship_conflict_cases_cardinality_check CHECK ((current_cardinality = ANY (ARRAY['one'::text, 'many'::text]))),
    CONSTRAINT relationship_conflict_cases_kind_check CHECK ((kind = 'cross_profile_current_state'::text)),
    CONSTRAINT relationship_conflict_cases_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_conflict_cases_polarity_check CHECK ((polarity = ANY (ARRAY['+'::text, '-'::text]))),
    CONSTRAINT relationship_conflict_cases_relationship_kind_check CHECK ((relationship_kind = ANY (ARRAY['state'::text, 'event'::text]))),
    CONSTRAINT relationship_conflict_cases_review_ttl_check CHECK (((review_ttl_days >= 1) AND (review_ttl_days <= 30))),
    CONSTRAINT relationship_conflict_cases_scope_nonempty CHECK ((btrim(semantic_scope_key) <> ''::text)),
    CONSTRAINT relationship_conflict_cases_status_check CHECK ((status = ANY (ARRAY['open'::text, 'overdue'::text, 'resolved'::text, 'dismissed'::text]))),
    CONSTRAINT relationship_conflict_cases_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.relationship_conflict_cases FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_cases relationship_conflict_cases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_cases
    ADD CONSTRAINT relationship_conflict_cases_pkey PRIMARY KEY (team_id, conflict_id);


--
-- Name: relationship_conflict_cases_due_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_cases_due_idx ON public.relationship_conflict_cases USING btree (team_id, next_review_at, conflict_id) WHERE (status = ANY (ARRAY['open'::text, 'overdue'::text]));


--
-- Name: relationship_conflict_cases_lease_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_cases_lease_idx ON public.relationship_conflict_cases USING btree (team_id, lease_until) WHERE ((status = ANY (ARRAY['open'::text, 'overdue'::text])) AND (lease_until IS NOT NULL));


--
-- Name: relationship_conflict_cases_open_scope_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_conflict_cases_open_scope_unique ON public.relationship_conflict_cases USING btree (team_id, semantic_scope_key) WHERE (status = ANY (ARRAY['open'::text, 'overdue'::text]));


--
-- Name: relationship_conflict_cases_subject_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_cases_subject_idx ON public.relationship_conflict_cases USING btree (team_id, subject_entity_id, predicate_key) WHERE (status = ANY (ARRAY['open'::text, 'overdue'::text, 'resolved'::text]));


--
-- Name: relationship_conflict_queue_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_queue_active_idx ON public.relationship_conflict_cases USING btree (team_id, status DESC, next_review_at, conflict_id) WHERE (status = ANY (ARRAY['open'::text, 'overdue'::text]));


--
-- Name: relationship_conflict_cases relationship_conflict_cases_team_id_predicate_key_predicat_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_cases
    ADD CONSTRAINT relationship_conflict_cases_team_id_predicate_key_predicat_fkey FOREIGN KEY (team_id, predicate_key, predicate_version) REFERENCES public.team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_cases relationship_conflict_cases_team_id_subject_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_cases
    ADD CONSTRAINT relationship_conflict_cases_team_id_subject_entity_id_fkey FOREIGN KEY (team_id, subject_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_cases; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_cases ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_cases relationship_conflict_cases_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_cases_insert ON public.relationship_conflict_cases FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_cases relationship_conflict_cases_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_cases_select ON public.relationship_conflict_cases FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_cases relationship_conflict_cases_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_cases_update ON public.relationship_conflict_cases FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
