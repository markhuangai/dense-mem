--
-- PostgreSQL database dump
--

\restrict DenseMemSchemaCatalogV1

-- Dumped from database version 18.4 (Debian 18.4-1.pgdg13+1)
-- Dumped by pg_dump version 18.4 (Debian 18.4-1.pgdg13+1)

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
-- Name: placement_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.placement_runs (
    team_id uuid NOT NULL,
    placement_run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 5 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_until timestamp with time zone,
    worker_id text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    assessor_attempt_id uuid,
    assessor_attempted_at timestamp with time zone,
    semantic_hold_state text,
    semantic_hold_version integer DEFAULT 0 NOT NULL,
    semantic_hold_updated_at timestamp with time zone,
    replaces_placement_run_id uuid,
    superseded_by_placement_run_id uuid,
    quarantine_expires_at timestamp with time zone,
    CONSTRAINT placement_runs_assessor_attempt_pair_check CHECK (((assessor_attempt_id IS NULL) = (assessor_attempted_at IS NULL))),
    CONSTRAINT placement_runs_attempts_check CHECK (((attempts >= 0) AND (max_attempts >= 1) AND (attempts <= max_attempts))),
    CONSTRAINT placement_runs_completion_check CHECK ((((status = ANY (ARRAY['awaiting_review'::text, 'completed'::text, 'failed'::text, 'quarantined'::text])) AND (completed_at IS NOT NULL)) OR (status <> ALL (ARRAY['awaiting_review'::text, 'completed'::text, 'failed'::text, 'quarantined'::text])))),
    CONSTRAINT placement_runs_quarantine_expiry_check CHECK (((status <> 'quarantined'::text) OR ((completed_at IS NOT NULL) AND (quarantine_expires_at IS NOT NULL) AND (quarantine_expires_at = (completed_at + '24:00:00'::interval))))),
    CONSTRAINT placement_runs_semantic_hold_state_check CHECK (((semantic_hold_state IS NULL) OR (semantic_hold_state = ANY (ARRAY['active'::text, 'expired'::text, 'superseded'::text])))),
    CONSTRAINT placement_runs_semantic_hold_version_check CHECK ((semantic_hold_version >= 0)),
    CONSTRAINT placement_runs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'guarded'::text, 'quarantined'::text, 'processing'::text, 'awaiting_review'::text, 'completed'::text, 'failed'::text])))
);

ALTER TABLE ONLY public.placement_runs FORCE ROW LEVEL SECURITY;


--
-- Name: placement_runs placement_runs_ingest_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_ingest_owner_ref_unique UNIQUE (team_id, placement_run_id, ingest_id, owner_profile_id);


--
-- Name: placement_runs placement_runs_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_owner_ref_unique UNIQUE (team_id, placement_run_id, owner_profile_id);


--
-- Name: placement_runs placement_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_pkey PRIMARY KEY (team_id, placement_run_id);


--
-- Name: placement_runs placement_runs_team_id_ingest_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_team_id_ingest_id_key UNIQUE (team_id, ingest_id);


--
-- Name: placement_runs_active_replacement_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_runs_active_replacement_unique ON public.placement_runs USING btree (team_id, replaces_placement_run_id) WHERE ((replaces_placement_run_id IS NOT NULL) AND (status = ANY (ARRAY['queued'::text, 'guarded'::text, 'processing'::text])));


--
-- Name: placement_runs_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_runs_owner_created_idx ON public.placement_runs USING btree (team_id, owner_profile_id, created_at DESC);


--
-- Name: placement_runs_replacement_target_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_runs_replacement_target_idx ON public.placement_runs USING btree (team_id, replaces_placement_run_id, created_at, placement_run_id) WHERE (replaces_placement_run_id IS NOT NULL);


--
-- Name: placement_runs_team_expired_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_runs_team_expired_claim_idx ON public.placement_runs USING btree (team_id, lease_until, created_at, placement_run_id) WHERE ((status = 'processing'::text) AND (lease_until IS NOT NULL) AND (attempts < max_attempts));


--
-- Name: placement_runs_team_status_available_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_runs_team_status_available_idx ON public.placement_runs USING btree (team_id, status, available_at, created_at, placement_run_id);


--
-- Name: placement_runs placement_runs_submission_hold_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE CONSTRAINT TRIGGER placement_runs_submission_hold_guard AFTER INSERT OR UPDATE OF status, ingest_id, owner_profile_id ON public.placement_runs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.ensure_submission_hold_for_awaiting_review();


--
-- Name: placement_runs placement_runs_replaces_run_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_replaces_run_ref FOREIGN KEY (team_id, replaces_placement_run_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_superseded_by_run_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_superseded_by_run_ref FOREIGN KEY (team_id, superseded_by_placement_run_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_runs placement_runs_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_runs
    ADD CONSTRAINT placement_runs_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: placement_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.placement_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: placement_runs placement_runs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_runs_insert ON public.placement_runs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_runs placement_runs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_runs_select ON public.placement_runs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_runs placement_runs_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_runs_update ON public.placement_runs FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
