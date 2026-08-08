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
-- Name: operation_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.operation_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    severity character varying(16) NOT NULL,
    severity_rank smallint NOT NULL,
    message text NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    team_id uuid,
    profile_id uuid,
    correlation_id text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    attrs jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.operation_logs FORCE ROW LEVEL SECURITY;


--
-- Name: operation_logs operation_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operation_logs
    ADD CONSTRAINT operation_logs_pkey PRIMARY KEY (id);


--
-- Name: idx_operation_logs_severity_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_operation_logs_severity_timestamp ON public.operation_logs USING btree (severity_rank DESC, "timestamp" DESC, id DESC);


--
-- Name: idx_operation_logs_severity_value_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_operation_logs_severity_value_timestamp ON public.operation_logs USING btree (severity, "timestamp" DESC, id DESC);


--
-- Name: idx_operation_logs_team_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_operation_logs_team_timestamp ON public.operation_logs USING btree (team_id, "timestamp" DESC) WHERE (team_id IS NOT NULL);


--
-- Name: idx_operation_logs_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_operation_logs_timestamp ON public.operation_logs USING btree ("timestamp" DESC, id DESC);


--
-- Name: operation_logs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.operation_logs ENABLE ROW LEVEL SECURITY;

--
-- Name: operation_logs operation_logs_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY operation_logs_system_access ON public.operation_logs USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
