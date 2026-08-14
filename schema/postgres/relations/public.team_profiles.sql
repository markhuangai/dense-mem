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
-- Name: team_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_profiles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    team_id uuid NOT NULL,
    key_hash text,
    key_prefix character varying(24),
    key_suffix character varying(6),
    name character varying(100) DEFAULT ''::character varying NOT NULL,
    scopes text[] DEFAULT ARRAY['read'::text, 'write'::text] NOT NULL,
    role character varying(20) DEFAULT 'member'::character varying NOT NULL,
    rate_limit integer DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auth_source character varying(20) DEFAULT 'api_key'::character varying NOT NULL,
    sso_identity_id uuid,
    sso_provider_id uuid,
    sso_subject text,
    sso_email text DEFAULT ''::text NOT NULL,
    sso_group_id text DEFAULT ''::text NOT NULL,
    sso_entitlement_status character varying(20) DEFAULT 'unlinked'::character varying NOT NULL,
    sso_last_entitlement_checked_at timestamp with time zone,
    sso_last_login_at timestamp with time zone,
    sso_owner_identity_id uuid,
    is_system boolean DEFAULT false NOT NULL,
    CONSTRAINT team_profiles_auth_source_check CHECK (((auth_source)::text = ANY ((ARRAY['api_key'::character varying, 'sso'::character varying, 'system'::character varying])::text[]))),
    CONSTRAINT team_profiles_auth_source_shape_check CHECK (((((auth_source)::text = 'api_key'::text) AND (key_hash IS NOT NULL) AND (key_prefix IS NOT NULL) AND (sso_identity_id IS NULL) AND (sso_provider_id IS NULL) AND (NULLIF(sso_subject, ''::text) IS NULL) AND ((sso_entitlement_status)::text = 'unlinked'::text)) OR (((auth_source)::text = 'sso'::text) AND (key_hash IS NULL) AND (key_prefix IS NULL) AND (NULLIF(sso_subject, ''::text) IS NOT NULL) AND ((sso_entitlement_status)::text = ANY ((ARRAY['active'::character varying, 'denied'::character varying, 'error'::character varying])::text[])) AND (((sso_identity_id IS NOT NULL) AND (sso_provider_id IS NOT NULL)) OR ((sso_identity_id IS NULL) AND (sso_provider_id IS NULL)))) OR (((auth_source)::text = 'system'::text) AND (key_hash IS NULL) AND (key_prefix IS NULL) AND (sso_identity_id IS NULL) AND (sso_provider_id IS NULL) AND (NULLIF(sso_subject, ''::text) IS NULL) AND ((sso_entitlement_status)::text = 'unlinked'::text) AND (revoked_at IS NOT NULL) AND is_system))),
    CONSTRAINT team_profiles_rate_limit_check CHECK ((rate_limit >= 0)),
    CONSTRAINT team_profiles_role_check CHECK (((role)::text = ANY ((ARRAY['manager'::character varying, 'member'::character varying])::text[]))),
    CONSTRAINT team_profiles_sso_entitlement_status_check CHECK (((sso_entitlement_status)::text = ANY ((ARRAY['unlinked'::character varying, 'active'::character varying, 'denied'::character varying, 'error'::character varying])::text[]))),
    CONSTRAINT team_profiles_system_marker_check CHECK ((((auth_source)::text = 'system'::text) = is_system))
);

ALTER TABLE ONLY public.team_profiles FORCE ROW LEVEL SECURITY;


--
-- Name: team_profiles team_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_pkey PRIMARY KEY (id);


--
-- Name: idx_team_profiles_key_prefix_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_key_prefix_unique ON public.team_profiles USING btree (key_prefix) WHERE (key_prefix IS NOT NULL);


--
-- Name: idx_team_profiles_sso_identity_team_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_sso_identity_team_unique ON public.team_profiles USING btree (sso_identity_id, team_id) WHERE (sso_identity_id IS NOT NULL);


--
-- Name: idx_team_profiles_sso_owner_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_team_profiles_sso_owner_identity ON public.team_profiles USING btree (sso_owner_identity_id) WHERE (sso_owner_identity_id IS NOT NULL);


--
-- Name: idx_team_profiles_sso_owner_team_active_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_sso_owner_team_active_unique ON public.team_profiles USING btree (sso_owner_identity_id, team_id) WHERE ((sso_owner_identity_id IS NOT NULL) AND ((auth_source)::text = 'api_key'::text) AND (revoked_at IS NULL));


--
-- Name: idx_team_profiles_sso_provider_subject; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_team_profiles_sso_provider_subject ON public.team_profiles USING btree (sso_provider_id, sso_subject) WHERE ((sso_provider_id IS NOT NULL) AND (sso_subject IS NOT NULL));


--
-- Name: idx_team_profiles_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_team_profiles_team_id ON public.team_profiles USING btree (team_id);


--
-- Name: idx_team_profiles_team_id_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_team_id_id_unique ON public.team_profiles USING btree (team_id, id);


--
-- Name: idx_team_profiles_team_name_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_team_profiles_team_name_unique ON public.team_profiles USING btree (team_id, lower((name)::text));


--
-- Name: team_profiles_system_team_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX team_profiles_system_team_unique ON public.team_profiles USING btree (team_id) WHERE is_system;


--
-- Name: team_profiles team_profiles_identity_bridge; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER team_profiles_identity_bridge AFTER INSERT OR DELETE OR UPDATE OF team_id, key_hash, key_prefix, key_suffix, name, scopes, role, expires_at, revoked_at, last_used_at ON public.team_profiles FOR EACH ROW EXECUTE FUNCTION public.dense_mem_sync_legacy_profile_identity();


--
-- Name: team_profiles team_profiles_sso_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_sso_identity_id_fkey FOREIGN KEY (sso_identity_id) REFERENCES public.sso_identities(id) ON DELETE SET NULL;


--
-- Name: team_profiles team_profiles_sso_owner_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_sso_owner_identity_id_fkey FOREIGN KEY (sso_owner_identity_id) REFERENCES public.sso_identities(id) ON DELETE SET NULL;


--
-- Name: team_profiles team_profiles_sso_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_sso_provider_id_fkey FOREIGN KEY (sso_provider_id) REFERENCES public.sso_providers(id) ON DELETE SET NULL;


--
-- Name: team_profiles team_profiles_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_profiles
    ADD CONSTRAINT team_profiles_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: team_profiles; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.team_profiles ENABLE ROW LEVEL SECURITY;

--
-- Name: team_profiles team_profiles_self_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_self_access ON public.team_profiles USING ((team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))) WITH CHECK ((team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)));


--
-- Name: team_profiles team_profiles_system_conflict_insert_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_system_conflict_insert_access ON public.team_profiles FOR INSERT WITH CHECK ((((current_setting('app.tx_mode'::text, true) = 'migration'::text) OR ((current_setting('app.tx_mode'::text, true) = 'system'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))) AND ((auth_source)::text = 'system'::text) AND is_system AND (revoked_at IS NOT NULL)));


--
-- Name: team_profiles team_profiles_system_read_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_system_read_access ON public.team_profiles FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- Name: team_profiles team_profiles_system_sso_insert_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_system_sso_insert_access ON public.team_profiles FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = 'system'::text) AND ((auth_source)::text = 'sso'::text) AND (sso_identity_id IS NOT NULL) AND (sso_provider_id IS NOT NULL) AND (NULLIF(sso_subject, ''::text) IS NOT NULL)));


--
-- Name: team_profiles team_profiles_system_update_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_profiles_system_update_access ON public.team_profiles FOR UPDATE USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
