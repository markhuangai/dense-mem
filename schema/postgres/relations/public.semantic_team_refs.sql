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
-- Name: semantic_team_refs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.semantic_team_refs (
    team_id uuid NOT NULL
);

ALTER TABLE ONLY public.semantic_team_refs FORCE ROW LEVEL SECURITY;


--
-- Name: semantic_team_refs semantic_team_refs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.semantic_team_refs
    ADD CONSTRAINT semantic_team_refs_pkey PRIMARY KEY (team_id);


--
-- Name: semantic_team_refs semantic_team_refs_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.semantic_team_refs
    ADD CONSTRAINT semantic_team_refs_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;


--
-- Name: semantic_team_refs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.semantic_team_refs ENABLE ROW LEVEL SECURITY;

--
-- Name: semantic_team_refs semantic_team_refs_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY semantic_team_refs_insert ON public.semantic_team_refs FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: semantic_team_refs semantic_team_refs_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY semantic_team_refs_select ON public.semantic_team_refs FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
