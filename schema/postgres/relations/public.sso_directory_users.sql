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
-- Name: sso_directory_users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connector_id uuid NOT NULL,
    external_id text DEFAULT ''::text NOT NULL,
    user_name text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    identity_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_users_display_name_check CHECK ((char_length(display_name) <= 512)),
    CONSTRAINT sso_directory_users_email_check CHECK ((char_length(email) <= 512)),
    CONSTRAINT sso_directory_users_external_id_check CHECK ((char_length(external_id) <= 512)),
    CONSTRAINT sso_directory_users_user_name_check CHECK ((btrim(user_name) <> ''::text)),
    CONSTRAINT sso_directory_users_user_name_check1 CHECK ((char_length(user_name) <= 512))
);

ALTER TABLE ONLY public.sso_directory_users FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_users sso_directory_users_connector_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_users
    ADD CONSTRAINT sso_directory_users_connector_id_id_key UNIQUE (connector_id, id);


--
-- Name: sso_directory_users sso_directory_users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_users
    ADD CONSTRAINT sso_directory_users_pkey PRIMARY KEY (id);


--
-- Name: idx_sso_directory_users_connector_external_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_directory_users_connector_external_id_unique ON public.sso_directory_users USING btree (connector_id, external_id) WHERE (external_id <> ''::text);


--
-- Name: idx_sso_directory_users_connector_username_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_directory_users_connector_username_unique ON public.sso_directory_users USING btree (connector_id, lower(user_name));


--
-- Name: sso_directory_users sso_directory_users_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_users
    ADD CONSTRAINT sso_directory_users_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_users sso_directory_users_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_users
    ADD CONSTRAINT sso_directory_users_identity_id_fkey FOREIGN KEY (identity_id) REFERENCES public.sso_identities(id) ON DELETE RESTRICT;


--
-- Name: sso_directory_users; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_users ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_users sso_directory_users_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_users_system_access ON public.sso_directory_users USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
