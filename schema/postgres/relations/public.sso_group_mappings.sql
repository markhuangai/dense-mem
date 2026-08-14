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
-- Name: sso_group_mappings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_group_mappings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    team_id uuid NOT NULL,
    group_id text NOT NULL,
    group_name text DEFAULT ''::text NOT NULL,
    scopes text[] DEFAULT ARRAY['read'::text] NOT NULL,
    role character varying(20) DEFAULT 'member'::character varying NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    origin text DEFAULT 'manual'::text NOT NULL,
    retired_at timestamp with time zone,
    CONSTRAINT sso_group_mappings_origin_check CHECK ((origin = ANY (ARRAY['manual'::text, 'directory'::text]))),
    CONSTRAINT sso_group_mappings_role_check CHECK (((role)::text = ANY ((ARRAY['manager'::character varying, 'member'::character varying])::text[])))
);

ALTER TABLE ONLY public.sso_group_mappings FORCE ROW LEVEL SECURITY;


--
-- Name: sso_group_mappings sso_group_mappings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_pkey PRIMARY KEY (id);


--
-- Name: sso_group_mappings sso_group_mappings_provider_id_team_id_group_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_provider_id_team_id_group_id_key UNIQUE (provider_id, team_id, group_id);


--
-- Name: idx_sso_group_mappings_provider_group; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_group_mappings_provider_group ON public.sso_group_mappings USING btree (provider_id, group_id) WHERE (enabled = true);


--
-- Name: sso_group_mappings sso_group_mappings_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_group_mappings sso_group_mappings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;


--
-- Name: sso_group_mappings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_group_mappings ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_group_mappings sso_group_mappings_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_group_mappings_system_access ON public.sso_group_mappings USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: sso_group_mappings sso_group_mappings_team_read; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_group_mappings_team_read ON public.sso_group_mappings FOR SELECT USING ((team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)));


--
-- PostgreSQL database dump complete
--
