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
-- Name: sso_control_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_control_sessions (
    session_hash text NOT NULL,
    identity_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    group_ids text[] DEFAULT ARRAY[]::text[] NOT NULL,
    csrf_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_control_sessions_csrf_hash_check CHECK ((char_length(csrf_hash) = 64)),
    CONSTRAINT sso_control_sessions_session_hash_check CHECK ((char_length(session_hash) = 64))
);

ALTER TABLE ONLY public.sso_control_sessions FORCE ROW LEVEL SECURITY;


--
-- Name: sso_control_sessions sso_control_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_sessions
    ADD CONSTRAINT sso_control_sessions_pkey PRIMARY KEY (session_hash);


--
-- Name: idx_sso_control_sessions_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_control_sessions_expiry ON public.sso_control_sessions USING btree (expires_at);


--
-- Name: sso_control_sessions sso_control_sessions_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_sessions
    ADD CONSTRAINT sso_control_sessions_identity_id_fkey FOREIGN KEY (identity_id) REFERENCES public.sso_identities(id) ON DELETE RESTRICT;


--
-- Name: sso_control_sessions sso_control_sessions_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_control_sessions
    ADD CONSTRAINT sso_control_sessions_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE RESTRICT;


--
-- Name: sso_control_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_control_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_control_sessions sso_control_sessions_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_control_sessions_system_access ON public.sso_control_sessions USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
