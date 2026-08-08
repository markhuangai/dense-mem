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
-- Name: sso_oauth_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_oauth_states (
    state_hash text NOT NULL,
    provider_id uuid NOT NULL,
    pkce_verifier text NOT NULL,
    nonce text NOT NULL,
    redirect_path text DEFAULT '/ui'::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.sso_oauth_states FORCE ROW LEVEL SECURITY;


--
-- Name: sso_oauth_states sso_oauth_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_oauth_states
    ADD CONSTRAINT sso_oauth_states_pkey PRIMARY KEY (state_hash);


--
-- Name: idx_sso_oauth_states_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_oauth_states_expires_at ON public.sso_oauth_states USING btree (expires_at);


--
-- Name: sso_oauth_states sso_oauth_states_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_oauth_states
    ADD CONSTRAINT sso_oauth_states_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_oauth_states; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_oauth_states ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_oauth_states sso_oauth_states_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_oauth_states_system_access ON public.sso_oauth_states USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
