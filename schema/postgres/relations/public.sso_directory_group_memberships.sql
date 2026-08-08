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
-- Name: sso_directory_group_memberships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_group_memberships (
    connector_id uuid NOT NULL,
    group_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.sso_directory_group_memberships FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_memberships
    ADD CONSTRAINT sso_directory_group_memberships_pkey PRIMARY KEY (connector_id, group_id, user_id);


--
-- Name: idx_sso_directory_group_memberships_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_directory_group_memberships_user ON public.sso_directory_group_memberships USING btree (connector_id, user_id);


--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_memberships
    ADD CONSTRAINT sso_directory_group_memberships_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_connector_id_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_memberships
    ADD CONSTRAINT sso_directory_group_memberships_connector_id_group_id_fkey FOREIGN KEY (connector_id, group_id) REFERENCES public.sso_directory_groups(connector_id, id) ON DELETE CASCADE;


--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_connector_id_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_memberships
    ADD CONSTRAINT sso_directory_group_memberships_connector_id_user_id_fkey FOREIGN KEY (connector_id, user_id) REFERENCES public.sso_directory_users(connector_id, id) ON DELETE CASCADE;


--
-- Name: sso_directory_group_memberships; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_group_memberships ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_group_memberships sso_directory_group_memberships_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_group_memberships_system_access ON public.sso_directory_group_memberships USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
