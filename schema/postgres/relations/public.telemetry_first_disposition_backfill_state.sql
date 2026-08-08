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
-- Name: telemetry_first_disposition_backfill_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.telemetry_first_disposition_backfill_state (
    state_key text NOT NULL,
    cursor_team_id uuid,
    cursor_ingest_id uuid,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT telemetry_first_disposition_backfill_state_cursor_check CHECK ((((cursor_team_id IS NULL) AND (cursor_ingest_id IS NULL)) OR ((cursor_team_id IS NOT NULL) AND (cursor_ingest_id IS NOT NULL))))
);

ALTER TABLE ONLY public.telemetry_first_disposition_backfill_state FORCE ROW LEVEL SECURITY;


--
-- Name: telemetry_first_disposition_backfill_state telemetry_first_disposition_backfill_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.telemetry_first_disposition_backfill_state
    ADD CONSTRAINT telemetry_first_disposition_backfill_state_pkey PRIMARY KEY (state_key);


--
-- Name: telemetry_first_disposition_backfill_state; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.telemetry_first_disposition_backfill_state ENABLE ROW LEVEL SECURITY;

--
-- Name: telemetry_first_disposition_backfill_state telemetry_first_disposition_backfill_state_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY telemetry_first_disposition_backfill_state_system_access ON public.telemetry_first_disposition_backfill_state USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
