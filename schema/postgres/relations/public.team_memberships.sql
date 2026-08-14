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
-- Name: team_memberships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_memberships (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    actor_identity_id uuid NOT NULL,
    team_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    team_admin boolean DEFAULT false NOT NULL,
    maximum_grants text[] DEFAULT ARRAY[]::text[] NOT NULL,
    legacy_profile_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT team_memberships_maximum_grants_check CHECK (((cardinality(maximum_grants) IS NULL) OR (cardinality(maximum_grants) <= 128))),
    CONSTRAINT team_memberships_status_check CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text, 'revoked'::text])))
);

ALTER TABLE ONLY public.team_memberships FORCE ROW LEVEL SECURITY;


--
-- Name: team_memberships team_memberships_actor_identity_id_team_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_memberships
    ADD CONSTRAINT team_memberships_actor_identity_id_team_id_key UNIQUE (actor_identity_id, team_id);


--
-- Name: team_memberships team_memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_memberships
    ADD CONSTRAINT team_memberships_pkey PRIMARY KEY (id);


--
-- Name: idx_team_memberships_legacy_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_memberships_legacy_profile ON public.team_memberships USING btree (legacy_profile_id) WHERE (legacy_profile_id IS NOT NULL);


--
-- Name: idx_team_memberships_team_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_team_memberships_team_status ON public.team_memberships USING btree (team_id, status, team_admin);


--
-- Name: team_memberships team_memberships_actor_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_memberships
    ADD CONSTRAINT team_memberships_actor_identity_id_fkey FOREIGN KEY (actor_identity_id) REFERENCES public.actor_identities(id) ON DELETE RESTRICT;


--
-- Name: team_memberships team_memberships_legacy_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_memberships
    ADD CONSTRAINT team_memberships_legacy_profile_id_fkey FOREIGN KEY (legacy_profile_id) REFERENCES public.team_profiles(id) ON DELETE SET NULL;


--
-- Name: team_memberships team_memberships_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_memberships
    ADD CONSTRAINT team_memberships_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: team_memberships; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.team_memberships ENABLE ROW LEVEL SECURITY;

--
-- Name: team_memberships team_memberships_context_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_memberships_context_access ON public.team_memberships USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
