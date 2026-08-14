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
-- Name: user_portal_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_portal_sessions (
    session_hash text NOT NULL,
    key_id uuid NOT NULL,
    csrf_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.user_portal_sessions FORCE ROW LEVEL SECURITY;


--
-- Name: user_portal_sessions user_portal_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_portal_sessions
    ADD CONSTRAINT user_portal_sessions_pkey PRIMARY KEY (session_hash);


--
-- Name: idx_user_portal_sessions_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_portal_sessions_expires_at ON public.user_portal_sessions USING btree (expires_at);


--
-- Name: idx_user_portal_sessions_key_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_portal_sessions_key_id ON public.user_portal_sessions USING btree (key_id);


--
-- Name: user_portal_sessions user_portal_sessions_key_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_portal_sessions
    ADD CONSTRAINT user_portal_sessions_key_id_fkey FOREIGN KEY (key_id) REFERENCES public.team_profiles(id) ON DELETE CASCADE;


--
-- Name: user_portal_sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.user_portal_sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: user_portal_sessions user_portal_sessions_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY user_portal_sessions_system_access ON public.user_portal_sessions USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
