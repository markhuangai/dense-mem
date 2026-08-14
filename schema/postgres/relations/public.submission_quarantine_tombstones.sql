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
-- Name: submission_quarantine_tombstones; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.submission_quarantine_tombstones (
    team_id uuid NOT NULL,
    fragment_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    content_hash text NOT NULL,
    tombstoned_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.submission_quarantine_tombstones FORCE ROW LEVEL SECURITY;


--
-- Name: submission_quarantine_tombstones submission_quarantine_tombstones_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_tombstones
    ADD CONSTRAINT submission_quarantine_tombstones_pkey PRIMARY KEY (team_id, fragment_id);


--
-- Name: submission_quarantine_tombstones submission_quarantine_tombsto_team_id_fragment_id_ingest_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.submission_quarantine_tombstones
    ADD CONSTRAINT submission_quarantine_tombsto_team_id_fragment_id_ingest_i_fkey FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: submission_quarantine_tombstones; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.submission_quarantine_tombstones ENABLE ROW LEVEL SECURITY;

--
-- Name: submission_quarantine_tombstones submission_quarantine_tombstones_owner_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_tombstones_owner_insert ON public.submission_quarantine_tombstones FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: submission_quarantine_tombstones submission_quarantine_tombstones_system_only; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY submission_quarantine_tombstones_system_only ON public.submission_quarantine_tombstones FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- PostgreSQL database dump complete
--
