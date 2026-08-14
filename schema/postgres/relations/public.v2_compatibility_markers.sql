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
-- Name: v2_compatibility_markers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_compatibility_markers (
    marker_id uuid DEFAULT gen_random_uuid() NOT NULL,
    marker_kind text NOT NULL,
    version text NOT NULL,
    status text NOT NULL,
    run_id uuid,
    corpus_hash text DEFAULT ''::text NOT NULL,
    gate_report_hash text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_compatibility_markers_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT v2_compatibility_markers_status_check CHECK ((status = ANY (ARRAY['compatible'::text, 'incompatible'::text, 'corrupt'::text])))
);

ALTER TABLE ONLY public.v2_compatibility_markers FORCE ROW LEVEL SECURITY;


--
-- Name: v2_compatibility_markers v2_compatibility_markers_marker_kind_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_compatibility_markers
    ADD CONSTRAINT v2_compatibility_markers_marker_kind_version_key UNIQUE (marker_kind, version);


--
-- Name: v2_compatibility_markers v2_compatibility_markers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_compatibility_markers
    ADD CONSTRAINT v2_compatibility_markers_pkey PRIMARY KEY (marker_id);


--
-- Name: idx_v2_compatibility_markers_kind_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_compatibility_markers_kind_created ON public.v2_compatibility_markers USING btree (marker_kind, created_at DESC);


--
-- Name: v2_compatibility_markers v2_compatibility_markers_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_compatibility_markers
    ADD CONSTRAINT v2_compatibility_markers_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE RESTRICT;


--
-- Name: v2_compatibility_markers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_compatibility_markers ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_compatibility_markers v2_compatibility_markers_system_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_compatibility_markers_system_insert ON public.v2_compatibility_markers FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND (marker_kind = 'v2_cutover'::text) AND (status = 'compatible'::text)));


--
-- Name: v2_compatibility_markers v2_compatibility_markers_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_compatibility_markers_system_select ON public.v2_compatibility_markers FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
