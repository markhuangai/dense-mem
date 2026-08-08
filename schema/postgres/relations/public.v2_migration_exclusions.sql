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
-- Name: v2_migration_exclusions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_exclusions (
    exclusion_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    source_kind text DEFAULT 'neo4j'::text NOT NULL,
    source_id text NOT NULL,
    reason text NOT NULL,
    blocks_cutover boolean DEFAULT true NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_exclusions_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.v2_migration_exclusions FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_exclusions v2_migration_exclusions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_exclusions
    ADD CONSTRAINT v2_migration_exclusions_pkey PRIMARY KEY (exclusion_id);


--
-- Name: v2_migration_exclusions v2_migration_exclusions_run_id_source_kind_source_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_exclusions
    ADD CONSTRAINT v2_migration_exclusions_run_id_source_kind_source_id_key UNIQUE (run_id, source_kind, source_id);


--
-- Name: v2_migration_exclusions v2_migration_exclusions_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_exclusions
    ADD CONSTRAINT v2_migration_exclusions_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_exclusions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_exclusions ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_exclusions v2_migration_exclusions_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_exclusions_system_select ON public.v2_migration_exclusions FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
