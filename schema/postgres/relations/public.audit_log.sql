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
-- Name: audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_log (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    team_id uuid,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    operation character varying(64) NOT NULL,
    entity_type character varying(64) NOT NULL,
    entity_id text NOT NULL,
    before_payload jsonb,
    after_payload jsonb,
    actor_profile_id uuid,
    actor_role character varying(20),
    client_ip inet,
    correlation_id text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL
);

ALTER TABLE ONLY public.audit_log FORCE ROW LEVEL SECURITY;


--
-- Name: audit_log audit_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);


--
-- Name: idx_audit_log_team_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_log_team_timestamp ON public.audit_log USING btree (team_id, "timestamp" DESC);


--
-- Name: idx_audit_log_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_log_timestamp ON public.audit_log USING btree ("timestamp" DESC);


--
-- Name: audit_log audit_log_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER audit_log_append_only BEFORE DELETE OR UPDATE ON public.audit_log FOR EACH ROW EXECUTE FUNCTION public.prevent_audit_log_mutation();


--
-- Name: audit_log; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.audit_log ENABLE ROW LEVEL SECURITY;

--
-- Name: audit_log audit_log_insert_all; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY audit_log_insert_all ON public.audit_log FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text, 'system'::text])) AND ((current_setting('app.tx_mode'::text, true) = 'system'::text) OR (team_id IS NULL) OR (team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))));


--
-- Name: audit_log audit_log_self_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY audit_log_self_access ON public.audit_log FOR SELECT USING ((team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)));


--
-- Name: audit_log audit_log_system_read_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY audit_log_system_read_access ON public.audit_log FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
