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
-- Name: v2_migration_gate_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_gate_results (
    gate_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    gate_name text NOT NULL,
    outcome text NOT NULL,
    evidence_ref text DEFAULT ''::text NOT NULL,
    evidence_hash text DEFAULT ''::text NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_gate_results_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT v2_migration_gate_results_outcome_check CHECK ((outcome = ANY (ARRAY['pass'::text, 'fail'::text, 'warning'::text])))
);

ALTER TABLE ONLY public.v2_migration_gate_results FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_gate_results v2_migration_gate_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_gate_results
    ADD CONSTRAINT v2_migration_gate_results_pkey PRIMARY KEY (gate_id);


--
-- Name: v2_migration_gate_results v2_migration_gate_results_run_id_gate_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_gate_results
    ADD CONSTRAINT v2_migration_gate_results_run_id_gate_name_key UNIQUE (run_id, gate_name);


--
-- Name: v2_migration_gate_results v2_migration_gate_results_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_gate_results
    ADD CONSTRAINT v2_migration_gate_results_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_gate_results; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_gate_results ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_gate_results v2_migration_gate_results_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_gate_results_system_select ON public.v2_migration_gate_results FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
