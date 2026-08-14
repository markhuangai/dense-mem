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
-- Name: membership_grants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.membership_grants (
    membership_id uuid NOT NULL,
    grant_name text NOT NULL,
    source text DEFAULT 'legacy_scope'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT membership_grants_grant_name_check CHECK (((char_length(grant_name) >= 1) AND (char_length(grant_name) <= 128))),
    CONSTRAINT membership_grants_source_check CHECK ((source = ANY (ARRAY['legacy_scope'::text, 'explicit'::text, 'system'::text])))
);

ALTER TABLE ONLY public.membership_grants FORCE ROW LEVEL SECURITY;


--
-- Name: membership_grants membership_grants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.membership_grants
    ADD CONSTRAINT membership_grants_pkey PRIMARY KEY (membership_id, grant_name);


--
-- Name: membership_grants membership_grants_membership_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.membership_grants
    ADD CONSTRAINT membership_grants_membership_id_fkey FOREIGN KEY (membership_id) REFERENCES public.team_memberships(id) ON DELETE CASCADE;


--
-- Name: membership_grants; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.membership_grants ENABLE ROW LEVEL SECURITY;

--
-- Name: membership_grants membership_grants_context_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY membership_grants_context_access ON public.membership_grants USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (EXISTS ( SELECT 1
   FROM public.team_memberships m
  WHERE ((m.id = membership_grants.membership_id) AND (m.team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))))))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (EXISTS ( SELECT 1
   FROM public.team_memberships m
  WHERE ((m.id = membership_grants.membership_id) AND (m.team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))))));


--
-- PostgreSQL database dump complete
--
