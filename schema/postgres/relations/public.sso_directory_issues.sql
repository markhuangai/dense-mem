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
-- Name: sso_directory_issues; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_issues (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connector_id uuid NOT NULL,
    group_id uuid,
    issue_key text NOT NULL,
    kind text NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_issues_detail_check CHECK ((char_length(detail) <= 1024)),
    CONSTRAINT sso_directory_issues_issue_key_check CHECK ((char_length(issue_key) <= 256)),
    CONSTRAINT sso_directory_issues_kind_check CHECK ((kind = ANY (ARRAY['invalid_group'::text, 'ambiguous_group'::text, 'team_collision'::text, 'auto_team_capacity'::text])))
);

ALTER TABLE ONLY public.sso_directory_issues FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_issues sso_directory_issues_connector_id_issue_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_issues
    ADD CONSTRAINT sso_directory_issues_connector_id_issue_key_key UNIQUE (connector_id, issue_key);


--
-- Name: sso_directory_issues sso_directory_issues_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_issues
    ADD CONSTRAINT sso_directory_issues_pkey PRIMARY KEY (id);


--
-- Name: sso_directory_issues sso_directory_issues_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_issues
    ADD CONSTRAINT sso_directory_issues_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_issues sso_directory_issues_connector_id_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_issues
    ADD CONSTRAINT sso_directory_issues_connector_id_group_id_fkey FOREIGN KEY (connector_id, group_id) REFERENCES public.sso_directory_groups(connector_id, id) ON DELETE CASCADE;


--
-- Name: sso_directory_issues; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_issues ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_issues sso_directory_issues_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_issues_system_access ON public.sso_directory_issues USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
