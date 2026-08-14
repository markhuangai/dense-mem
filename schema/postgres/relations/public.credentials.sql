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
-- Name: credentials; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credentials (
    id uuid NOT NULL,
    actor_identity_id uuid NOT NULL,
    owner_identity_id uuid,
    team_id uuid NOT NULL,
    kind text DEFAULT 'api_key'::text NOT NULL,
    key_hash text,
    key_prefix character varying(24),
    key_suffix character varying(6),
    name character varying(100) DEFAULT ''::character varying NOT NULL,
    scopes text[] DEFAULT ARRAY[]::text[] NOT NULL,
    rate_limit integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    legacy_profile_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    CONSTRAINT credentials_check CHECK (((kind <> 'api_key'::text) OR ((key_hash IS NOT NULL) AND (key_prefix IS NOT NULL)))),
    CONSTRAINT credentials_kind_check CHECK ((kind = ANY (ARRAY['api_key'::text, 'oauth'::text, 'session'::text, 'system'::text]))),
    CONSTRAINT credentials_name_check CHECK ((char_length((name)::text) <= 100)),
    CONSTRAINT credentials_rate_limit_check CHECK ((rate_limit >= 0)),
    CONSTRAINT credentials_scopes_check CHECK (((cardinality(scopes) IS NULL) OR (cardinality(scopes) <= 128))),
    CONSTRAINT credentials_status_check CHECK ((status = ANY (ARRAY['active'::text, 'revoked'::text, 'expired'::text, 'disabled'::text])))
);

ALTER TABLE ONLY public.credentials FORCE ROW LEVEL SECURITY;


--
-- Name: credentials credentials_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_pkey PRIMARY KEY (id);


--
-- Name: idx_credentials_key_prefix_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_credentials_key_prefix_unique ON public.credentials USING btree (key_prefix) WHERE (key_prefix IS NOT NULL);


--
-- Name: idx_credentials_legacy_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_credentials_legacy_profile ON public.credentials USING btree (legacy_profile_id) WHERE (legacy_profile_id IS NOT NULL);


--
-- Name: idx_credentials_owner_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_owner_identity ON public.credentials USING btree (owner_identity_id) WHERE (owner_identity_id IS NOT NULL);


--
-- Name: idx_credentials_team_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_team_status ON public.credentials USING btree (team_id, status, created_at DESC);


--
-- Name: credentials credentials_actor_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_actor_identity_id_fkey FOREIGN KEY (actor_identity_id) REFERENCES public.actor_identities(id) ON DELETE RESTRICT;


--
-- Name: credentials credentials_legacy_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_legacy_profile_id_fkey FOREIGN KEY (legacy_profile_id) REFERENCES public.team_profiles(id) ON DELETE SET NULL;


--
-- Name: credentials credentials_owner_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_owner_identity_id_fkey FOREIGN KEY (owner_identity_id) REFERENCES public.actor_identities(id) ON DELETE SET NULL;


--
-- Name: credentials credentials_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: credentials; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.credentials ENABLE ROW LEVEL SECURITY;

--
-- Name: credentials credentials_context_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY credentials_context_access ON public.credentials USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
