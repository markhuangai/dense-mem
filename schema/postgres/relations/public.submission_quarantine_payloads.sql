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
-- Name: submission_quarantine_payloads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.submission_quarantine_payloads (
    team_id uuid NOT NULL,
    quarantine_payload_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_run_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    proposal jsonb DEFAULT '{}'::jsonb NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    assessor_response jsonb DEFAULT '{}'::jsonb NOT NULL,
    payload_sha256 text DEFAULT ''::text NOT NULL,
    quarantined_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT submission_quarantine_payloads_expiry_check CHECK ((expires_at = (quarantined_at + '24:00:00'::interval))),
    CONSTRAINT submission_quarantine_payloads_json_check CHECK (((jsonb_typeof(proposal) = 'object'::text) AND (jsonb_typeof(evidence) = 'array'::text) AND (jsonb_typeof(assessor_response) = 'object'::text)))
);

ALTER TABLE ONLY public.submission_quarantine_payloads FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE submission_quarantine_payloads; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.submission_quarantine_payloads IS 'System/migration-only raw quarantined submission payload copy. Purge after exactly 24 hours; immutable source ledger rows remain for audit and lineage; public reads are forbidden.';


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_payloads
    ADD CONSTRAINT submission_quarantine_payloads_pkey PRIMARY KEY (team_id, quarantine_payload_id);


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_team_id_placement_run_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_payloads
    ADD CONSTRAINT submission_quarantine_payloads_team_id_placement_run_id_key UNIQUE (team_id, placement_run_id);


--
-- Name: submission_quarantine_payloads_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX submission_quarantine_payloads_expiry_idx ON public.submission_quarantine_payloads USING btree (expires_at, team_id, quarantine_payload_id);


--
-- Name: submission_quarantine_payloads submission_quarantine_payload_team_id_placement_run_id_ing_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_payloads
    ADD CONSTRAINT submission_quarantine_payload_team_id_placement_run_id_ing_fkey FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: submission_quarantine_payloads; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.submission_quarantine_payloads ENABLE ROW LEVEL SECURITY;

--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_owner_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_payloads_owner_insert ON public.submission_quarantine_payloads FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_system_delete; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_payloads_system_delete ON public.submission_quarantine_payloads FOR DELETE USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_system_only; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_payloads_system_only ON public.submission_quarantine_payloads FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- Name: submission_quarantine_payloads submission_quarantine_payloads_system_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_payloads_system_update ON public.submission_quarantine_payloads FOR UPDATE USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text]))) WITH CHECK ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
