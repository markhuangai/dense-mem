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
-- Name: v2_migration_checkpoints; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_checkpoints (
    run_id uuid NOT NULL,
    checkpoint_key text NOT NULL,
    checkpoint_value jsonb DEFAULT '{}'::jsonb NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_checkpoints_json_check CHECK ((jsonb_typeof(checkpoint_value) = 'object'::text))
);

ALTER TABLE ONLY public.v2_migration_checkpoints FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_checkpoints v2_migration_checkpoints_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_checkpoints
    ADD CONSTRAINT v2_migration_checkpoints_pkey PRIMARY KEY (run_id, checkpoint_key);


--
-- Name: v2_migration_checkpoints v2_migration_checkpoints_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_checkpoints
    ADD CONSTRAINT v2_migration_checkpoints_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_checkpoints; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_checkpoints ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_checkpoints v2_migration_checkpoints_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_checkpoints_system_select ON public.v2_migration_checkpoints FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
