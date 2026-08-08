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
-- Name: teams; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.teams (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(100) NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    status character varying(20) DEFAULT 'active'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    config_version bigint DEFAULT 1 NOT NULL,
    directory_connector_id uuid,
    directory_group_id uuid,
    directory_managed boolean DEFAULT false NOT NULL,
    CONSTRAINT teams_config_version_check CHECK ((config_version >= 1)),
    CONSTRAINT teams_directory_managed_shape_check CHECK ((((NOT directory_managed) AND (directory_connector_id IS NULL) AND (directory_group_id IS NULL)) OR (directory_managed AND (directory_connector_id IS NOT NULL) AND (directory_group_id IS NOT NULL)))),
    CONSTRAINT teams_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('archived'::character varying)::text, ('deleted'::character varying)::text])))
);

ALTER TABLE ONLY public.teams FORCE ROW LEVEL SECURITY;


--
-- Name: teams teams_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_pkey PRIMARY KEY (id);


--
-- Name: idx_teams_directory_managed_group_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_teams_directory_managed_group_unique ON public.teams USING btree (directory_connector_id, directory_group_id) WHERE directory_managed;


--
-- Name: idx_teams_name_unique_active; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_teams_name_unique_active ON public.teams USING btree (lower((name)::text)) WHERE (deleted_at IS NULL);


--
-- Name: teams teams_directory_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_directory_connector_id_fkey FOREIGN KEY (directory_connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE RESTRICT;


--
-- Name: teams teams_directory_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_directory_group_id_fkey FOREIGN KEY (directory_group_id) REFERENCES public.sso_directory_groups(id) ON DELETE RESTRICT;


--
-- Name: teams; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.teams ENABLE ROW LEVEL SECURITY;

--
-- Name: teams teams_directory_system_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_directory_system_insert ON public.teams FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND ((directory_managed AND (directory_connector_id IS NOT NULL) AND (directory_group_id IS NOT NULL)) OR ((NOT directory_managed) AND (directory_connector_id IS NULL) AND (directory_group_id IS NULL)))));


--
-- Name: teams teams_directory_system_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_directory_system_update ON public.teams FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND directory_managed)) WITH CHECK (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND ((directory_managed AND (directory_connector_id IS NOT NULL) AND (directory_group_id IS NOT NULL)) OR ((NOT directory_managed) AND (directory_connector_id IS NULL) AND (directory_group_id IS NULL)))));


--
-- Name: teams teams_self_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_self_access ON public.teams USING ((id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))) WITH CHECK ((id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)));


--
-- Name: teams teams_system_read_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY teams_system_read_access ON public.teams FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
