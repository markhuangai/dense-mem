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
-- Name: dream_cycle_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dream_cycle_runs (
    team_id uuid NOT NULL,
    run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    initiated_by_profile_id uuid,
    run_date text NOT NULL,
    window_key text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    lease_until timestamp with time zone,
    input_count integer DEFAULT 0 NOT NULL,
    created_hypotheses integer DEFAULT 0 NOT NULL,
    rejected_hypotheses integer DEFAULT 0 NOT NULL,
    source_snapshot jsonb DEFAULT '[]'::jsonb NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    canonical_run_id uuid,
    scheduled_for timestamp with time zone,
    lease_token uuid,
    attempt_count integer DEFAULT 0 NOT NULL,
    provider_model text DEFAULT ''::text NOT NULL,
    provider_turns integer DEFAULT 0 NOT NULL,
    provider_input_tokens integer DEFAULT 0 NOT NULL,
    provider_output_tokens integer DEFAULT 0 NOT NULL,
    attempted_paths integer DEFAULT 0 NOT NULL,
    provider_proposals integer DEFAULT 0 NOT NULL,
    outcome_summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT dream_cycle_runs_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT dream_cycle_runs_counts_check CHECK (((input_count >= 0) AND (created_hypotheses >= 0) AND (rejected_hypotheses >= 0))),
    CONSTRAINT dream_cycle_runs_outcome_summary_object_check CHECK ((jsonb_typeof(outcome_summary) = 'object'::text)),
    CONSTRAINT dream_cycle_runs_provider_counts_check CHECK (((provider_turns >= 0) AND (provider_input_tokens >= 0) AND (provider_output_tokens >= 0) AND (attempted_paths >= 0) AND (provider_proposals >= 0))),
    CONSTRAINT dream_cycle_runs_snapshot_array_check CHECK ((jsonb_typeof(source_snapshot) = 'array'::text)),
    CONSTRAINT dream_cycle_runs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'completed'::text, 'failed'::text, 'skipped'::text, 'cancelled'::text, 'missed'::text]))),
    CONSTRAINT dream_cycle_runs_window_nonempty CHECK ((btrim(window_key) <> ''::text))
);

ALTER TABLE ONLY public.dream_cycle_runs FORCE ROW LEVEL SECURITY;


--
-- Name: dream_cycle_runs dream_cycle_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_pkey PRIMARY KEY (team_id, run_id);


--
-- Name: dream_cycle_runs_due_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dream_cycle_runs_due_idx ON public.dream_cycle_runs USING btree (team_id, started_at DESC) WHERE (canonical_run_id IS NULL);


--
-- Name: dream_cycle_runs_recovery_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dream_cycle_runs_recovery_idx ON public.dream_cycle_runs USING btree (team_id, lease_until, started_at) WHERE ((canonical_run_id IS NULL) AND (status = 'running'::text));


--
-- Name: dream_cycle_runs_team_window_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX dream_cycle_runs_team_window_canonical_unique ON public.dream_cycle_runs USING btree (team_id, window_key) WHERE (canonical_run_id IS NULL);


--
-- Name: dream_cycle_runs dream_cycle_runs_canonical_run_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_canonical_run_fk FOREIGN KEY (team_id, canonical_run_id) REFERENCES public.dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT;


--
-- Name: dream_cycle_runs dream_cycle_runs_team_id_initiated_by_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_cycle_runs
    ADD CONSTRAINT dream_cycle_runs_team_id_initiated_by_profile_id_fkey FOREIGN KEY (team_id, initiated_by_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: dream_cycle_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.dream_cycle_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: dream_cycle_runs dream_cycle_runs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_cycle_runs_insert ON public.dream_cycle_runs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (initiated_by_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: dream_cycle_runs dream_cycle_runs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_cycle_runs_select ON public.dream_cycle_runs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: dream_cycle_runs dream_cycle_runs_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_cycle_runs_update ON public.dream_cycle_runs FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (initiated_by_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (initiated_by_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
