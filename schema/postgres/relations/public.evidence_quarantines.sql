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
-- Name: evidence_quarantines; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_quarantines (
    team_id uuid NOT NULL,
    quarantine_id uuid DEFAULT gen_random_uuid() NOT NULL,
    fragment_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    released_by_profile_id uuid,
    release_reason text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    released_at timestamp with time zone,
    CONSTRAINT evidence_quarantines_release_check CHECK ((((status = 'active'::text) AND (released_at IS NULL)) OR ((status = 'released'::text) AND (released_at IS NOT NULL) AND (released_by_profile_id IS NOT NULL)))),
    CONSTRAINT evidence_quarantines_status_check CHECK ((status = ANY (ARRAY['active'::text, 'released'::text])))
);

ALTER TABLE ONLY public.evidence_quarantines FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_quarantines evidence_quarantines_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_pkey PRIMARY KEY (team_id, quarantine_id);


--
-- Name: evidence_quarantines evidence_quarantines_team_id_fragment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_fragment_id_key UNIQUE (team_id, fragment_id);


--
-- Name: evidence_quarantines_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX evidence_quarantines_status_idx ON public.evidence_quarantines USING btree (team_id, status, created_at);


--
-- Name: evidence_quarantines evidence_quarantines_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_fragment_id_ingest_id_owner_p_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_fragment_id_ingest_id_owner_p_fkey FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines evidence_quarantines_team_id_released_by_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_quarantines
    ADD CONSTRAINT evidence_quarantines_team_id_released_by_profile_id_fkey FOREIGN KEY (team_id, released_by_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_quarantines; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_quarantines ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_quarantines evidence_quarantines_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_quarantines_insert ON public.evidence_quarantines FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_quarantines evidence_quarantines_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_quarantines_select ON public.evidence_quarantines FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_quarantines evidence_quarantines_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_quarantines_update ON public.evidence_quarantines FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
