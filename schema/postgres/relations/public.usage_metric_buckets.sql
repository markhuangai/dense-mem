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
-- Name: usage_metric_buckets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_metric_buckets (
    bucket_start timestamp with time zone NOT NULL,
    team_id uuid NOT NULL,
    key_id uuid NOT NULL,
    route text NOT NULL,
    method character varying(16) NOT NULL,
    status_class smallint NOT NULL,
    request_count bigint DEFAULT 0 NOT NULL,
    error_count bigint DEFAULT 0 NOT NULL,
    total_latency_ms bigint DEFAULT 0 NOT NULL,
    max_latency_ms bigint DEFAULT 0 NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT usage_metric_buckets_error_count_check CHECK ((error_count >= 0)),
    CONSTRAINT usage_metric_buckets_max_latency_ms_check CHECK ((max_latency_ms >= 0)),
    CONSTRAINT usage_metric_buckets_request_count_check CHECK ((request_count >= 0)),
    CONSTRAINT usage_metric_buckets_status_class_check CHECK (((status_class >= 1) AND (status_class <= 5))),
    CONSTRAINT usage_metric_buckets_total_latency_ms_check CHECK ((total_latency_ms >= 0))
);

ALTER TABLE ONLY public.usage_metric_buckets FORCE ROW LEVEL SECURITY;


--
-- Name: usage_metric_buckets usage_metric_buckets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_metric_buckets
    ADD CONSTRAINT usage_metric_buckets_pkey PRIMARY KEY (bucket_start, team_id, key_id, route, method, status_class);


--
-- Name: idx_usage_metric_buckets_bucket_start; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_metric_buckets_bucket_start ON public.usage_metric_buckets USING btree (bucket_start DESC);


--
-- Name: idx_usage_metric_buckets_key_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_metric_buckets_key_bucket ON public.usage_metric_buckets USING btree (key_id, bucket_start DESC);


--
-- Name: idx_usage_metric_buckets_team_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_metric_buckets_team_bucket ON public.usage_metric_buckets USING btree (team_id, bucket_start DESC);


--
-- Name: usage_metric_buckets usage_metric_buckets_key_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_metric_buckets
    ADD CONSTRAINT usage_metric_buckets_key_id_fkey FOREIGN KEY (key_id) REFERENCES public.team_profiles(id) ON DELETE CASCADE;


--
-- Name: usage_metric_buckets usage_metric_buckets_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_metric_buckets
    ADD CONSTRAINT usage_metric_buckets_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;


--
-- Name: usage_metric_buckets; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.usage_metric_buckets ENABLE ROW LEVEL SECURITY;

--
-- Name: usage_metric_buckets usage_metric_buckets_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY usage_metric_buckets_system_access ON public.usage_metric_buckets USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: usage_metric_buckets usage_metric_buckets_team_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY usage_metric_buckets_team_read ON public.usage_metric_buckets FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = 'team'::text) AND ((team_id)::text = current_setting('app.current_team_id'::text, true))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
