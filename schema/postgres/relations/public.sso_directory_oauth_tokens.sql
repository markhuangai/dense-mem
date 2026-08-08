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
-- Name: sso_directory_oauth_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_oauth_tokens (
    token_hash text NOT NULL,
    connector_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_oauth_tokens_token_hash_check CHECK ((char_length(token_hash) = 64))
);

ALTER TABLE ONLY public.sso_directory_oauth_tokens FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_oauth_tokens sso_directory_oauth_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_oauth_tokens
    ADD CONSTRAINT sso_directory_oauth_tokens_pkey PRIMARY KEY (token_hash);


--
-- Name: idx_sso_directory_oauth_tokens_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_directory_oauth_tokens_expires_at ON public.sso_directory_oauth_tokens USING btree (expires_at);


--
-- Name: sso_directory_oauth_tokens sso_directory_oauth_tokens_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_oauth_tokens
    ADD CONSTRAINT sso_directory_oauth_tokens_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_oauth_tokens; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_oauth_tokens ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_oauth_tokens sso_directory_oauth_tokens_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_oauth_tokens_system_access ON public.sso_directory_oauth_tokens USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
