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
-- Name: value_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.value_records (
    team_id uuid NOT NULL,
    value_id uuid DEFAULT gen_random_uuid() NOT NULL,
    value_type text NOT NULL,
    canonical_value text NOT NULL,
    unit text,
    display text DEFAULT ''::text NOT NULL,
    normalization_version integer DEFAULT 1 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT value_records_canonical_nonempty CHECK ((btrim(canonical_value) <> ''::text)),
    CONSTRAINT value_records_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT value_records_normalization_version_check CHECK ((normalization_version >= 1)),
    CONSTRAINT value_records_type_check CHECK ((value_type = ANY (ARRAY['string'::text, 'number'::text, 'boolean'::text, 'date'::text, 'date_time'::text])))
);

ALTER TABLE ONLY public.value_records FORCE ROW LEVEL SECURITY;


--
-- Name: value_records value_records_canonical_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.value_records
    ADD CONSTRAINT value_records_canonical_unique UNIQUE NULLS NOT DISTINCT (team_id, value_type, canonical_value, unit, normalization_version);


--
-- Name: value_records value_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.value_records
    ADD CONSTRAINT value_records_pkey PRIMARY KEY (team_id, value_id);


--
-- Name: value_records value_records_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.value_records
    ADD CONSTRAINT value_records_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


--
-- Name: value_records; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.value_records ENABLE ROW LEVEL SECURITY;

--
-- Name: value_records value_records_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY value_records_insert ON public.value_records FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: value_records value_records_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY value_records_select ON public.value_records FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: value_records value_records_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY value_records_update ON public.value_records FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
