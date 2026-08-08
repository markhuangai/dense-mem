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
-- Name: submission_holds; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.submission_holds (
    team_id uuid NOT NULL,
    submission_hold_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_run_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    assessment_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    reason_code text NOT NULL,
    held_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT submission_holds_expiry_check CHECK ((expires_at = (held_at + '24:00:00'::interval))),
    CONSTRAINT submission_holds_reason_nonempty CHECK ((btrim(reason_code) <> ''::text)),
    CONSTRAINT submission_holds_time_order_check CHECK ((expires_at > held_at))
);

ALTER TABLE ONLY public.submission_holds FORCE ROW LEVEL SECURITY;


--
-- Name: submission_holds submission_holds_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_pkey PRIMARY KEY (team_id, submission_hold_id);


--
-- Name: submission_holds submission_holds_team_id_assessment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_assessment_id_key UNIQUE (team_id, assessment_id);


--
-- Name: submission_holds submission_holds_team_id_placement_run_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_placement_run_id_key UNIQUE (team_id, placement_run_id);


--
-- Name: submission_holds_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX submission_holds_expiry_idx ON public.submission_holds USING btree (team_id, expires_at, placement_run_id);


--
-- Name: submission_holds_owner_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX submission_holds_owner_expiry_idx ON public.submission_holds USING btree (team_id, owner_profile_id, expires_at, placement_run_id);


--
-- Name: submission_holds submission_holds_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER submission_holds_append_only BEFORE DELETE OR UPDATE ON public.submission_holds FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: submission_holds submission_holds_team_id_assessment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_assessment_id_fkey FOREIGN KEY (team_id, assessment_id) REFERENCES public.placement_assessments(team_id, assessment_id) ON DELETE RESTRICT;


--
-- Name: submission_holds submission_holds_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: submission_holds submission_holds_team_id_placement_run_id_ingest_id_owner__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_holds
    ADD CONSTRAINT submission_holds_team_id_placement_run_id_ingest_id_owner__fkey FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: submission_holds; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.submission_holds ENABLE ROW LEVEL SECURITY;

--
-- Name: submission_holds submission_holds_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_holds_insert ON public.submission_holds FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: submission_holds submission_holds_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_holds_select ON public.submission_holds FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
