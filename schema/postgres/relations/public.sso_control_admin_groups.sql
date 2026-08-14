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
-- Name: sso_control_admin_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_control_admin_groups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    group_id text NOT NULL,
    group_name text DEFAULT ''::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    retired_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_control_admin_groups_group_id_check CHECK ((char_length(group_id) <= 512)),
    CONSTRAINT sso_control_admin_groups_group_name_check CHECK ((char_length(group_name) <= 512))
);

ALTER TABLE ONLY public.sso_control_admin_groups FORCE ROW LEVEL SECURITY;


--
-- Name: sso_control_admin_groups sso_control_admin_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_admin_groups
    ADD CONSTRAINT sso_control_admin_groups_pkey PRIMARY KEY (id);


--
-- Name: sso_control_admin_groups sso_control_admin_groups_provider_id_group_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_admin_groups
    ADD CONSTRAINT sso_control_admin_groups_provider_id_group_id_key UNIQUE (provider_id, group_id);


--
-- Name: idx_sso_control_admin_groups_provider_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_control_admin_groups_provider_active ON public.sso_control_admin_groups USING btree (provider_id, group_id) WHERE (enabled AND (retired_at IS NULL));


--
-- Name: sso_control_admin_groups sso_control_admin_groups_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_admin_groups
    ADD CONSTRAINT sso_control_admin_groups_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE RESTRICT;


--
-- Name: sso_control_admin_groups; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_control_admin_groups ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_control_admin_groups sso_control_admin_groups_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_control_admin_groups_system_access ON public.sso_control_admin_groups USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
