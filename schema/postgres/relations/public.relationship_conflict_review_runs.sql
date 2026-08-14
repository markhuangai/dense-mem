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
-- Name: relationship_conflict_review_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_review_runs (
    team_id uuid NOT NULL,
    review_run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    local_run_date date NOT NULL,
    policy_version text DEFAULT 'cross_profile_conflict_v1'::text NOT NULL,
    status text DEFAULT 'reserved'::text NOT NULL,
    worker_id text DEFAULT ''::text NOT NULL,
    timezone text DEFAULT 'Local'::text NOT NULL,
    lease_until timestamp with time zone,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    claimed_cases integer DEFAULT 0 NOT NULL,
    resolved_cases integer DEFAULT 0 NOT NULL,
    overdue_cases integer DEFAULT 0 NOT NULL,
    no_op_cases integer DEFAULT 0 NOT NULL,
    failed_cases integer DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_review_runs_counts_check CHECK (((claimed_cases >= 0) AND (resolved_cases >= 0) AND (overdue_cases >= 0) AND (no_op_cases >= 0) AND (failed_cases >= 0))),
    CONSTRAINT relationship_conflict_review_runs_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_conflict_review_runs_status_check CHECK ((status = ANY (ARRAY['reserved'::text, 'running'::text, 'completed'::text, 'failed'::text])))
);

ALTER TABLE ONLY public.relationship_conflict_review_runs FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_review_runs relationship_conflict_review__team_id_local_run_date_policy_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_review_runs
    ADD CONSTRAINT relationship_conflict_review__team_id_local_run_date_policy_key UNIQUE (team_id, local_run_date, policy_version);


--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_review_runs
    ADD CONSTRAINT relationship_conflict_review_runs_pkey PRIMARY KEY (team_id, review_run_id);


--
-- Name: relationship_conflict_review_runs_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_review_runs_status_idx ON public.relationship_conflict_review_runs USING btree (team_id, status, lease_until);


--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_review_runs
    ADD CONSTRAINT relationship_conflict_review_runs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_review_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_review_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_review_runs_insert ON public.relationship_conflict_review_runs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_review_runs_select ON public.relationship_conflict_review_runs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_review_runs relationship_conflict_review_runs_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_review_runs_update ON public.relationship_conflict_review_runs FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
