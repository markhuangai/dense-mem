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
-- Name: sso_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_sessions (
    session_hash text NOT NULL,
    identity_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    team_profile_id uuid NOT NULL,
    team_id uuid NOT NULL,
    csrf_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.sso_sessions FORCE ROW LEVEL SECURITY;


--
-- Name: sso_sessions sso_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_pkey PRIMARY KEY (session_hash);


--
-- Name: idx_sso_sessions_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_sessions_expires_at ON public.sso_sessions USING btree (expires_at);


--
-- Name: idx_sso_sessions_identity_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_sessions_identity_id ON public.sso_sessions USING btree (identity_id);


--
-- Name: sso_sessions sso_sessions_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_identity_id_fkey FOREIGN KEY (identity_id) REFERENCES public.sso_identities(id) ON DELETE CASCADE;


--
-- Name: sso_sessions sso_sessions_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_sessions sso_sessions_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;


--
-- Name: sso_sessions sso_sessions_team_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_sessions
    ADD CONSTRAINT sso_sessions_team_profile_id_fkey FOREIGN KEY (team_profile_id) REFERENCES public.team_profiles(id) ON DELETE CASCADE;


--
-- Name: sso_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_sessions sso_sessions_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_sessions_system_access ON public.sso_sessions USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
