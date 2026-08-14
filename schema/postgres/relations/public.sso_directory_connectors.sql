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
-- Name: sso_directory_connectors; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_connectors (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    status text DEFAULT 'disabled'::text NOT NULL,
    group_pattern text NOT NULL,
    role_entitlements jsonb DEFAULT '{}'::jsonb NOT NULL,
    max_auto_teams integer DEFAULT 100 NOT NULL,
    credential_version integer DEFAULT 1 NOT NULL,
    bearer_token_hash text DEFAULT ''::text NOT NULL,
    oauth_client_id text DEFAULT ''::text NOT NULL,
    oauth_client_secret_hash text DEFAULT ''::text NOT NULL,
    last_activation_at timestamp with time zone,
    reconcile_version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_connectors_bearer_token_hash_check CHECK ((char_length(bearer_token_hash) <= 512)),
    CONSTRAINT sso_directory_connectors_credential_version_check CHECK ((credential_version >= 1)),
    CONSTRAINT sso_directory_connectors_group_pattern_check CHECK ((char_length(group_pattern) <= 1024)),
    CONSTRAINT sso_directory_connectors_max_auto_teams_check CHECK (((max_auto_teams >= 1) AND (max_auto_teams <= 1000))),
    CONSTRAINT sso_directory_connectors_oauth_client_id_check CHECK ((char_length(oauth_client_id) <= 128)),
    CONSTRAINT sso_directory_connectors_oauth_client_secret_hash_check CHECK ((char_length(oauth_client_secret_hash) <= 512)),
    CONSTRAINT sso_directory_connectors_reconcile_version_check CHECK ((reconcile_version >= 1)),
    CONSTRAINT sso_directory_connectors_role_entitlements_check CHECK ((jsonb_typeof(role_entitlements) = 'object'::text)),
    CONSTRAINT sso_directory_connectors_status_check CHECK ((status = ANY (ARRAY['disabled'::text, 'observe'::text, 'active'::text])))
);

ALTER TABLE ONLY public.sso_directory_connectors FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_connectors sso_directory_connectors_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_connectors
    ADD CONSTRAINT sso_directory_connectors_pkey PRIMARY KEY (id);


--
-- Name: sso_directory_connectors sso_directory_connectors_provider_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_connectors
    ADD CONSTRAINT sso_directory_connectors_provider_id_key UNIQUE (provider_id);


--
-- Name: idx_sso_directory_connectors_oauth_client_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_directory_connectors_oauth_client_id_unique ON public.sso_directory_connectors USING btree (oauth_client_id) WHERE (oauth_client_id <> ''::text);


--
-- Name: sso_directory_connectors sso_directory_connectors_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_connectors
    ADD CONSTRAINT sso_directory_connectors_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE RESTRICT;


--
-- Name: sso_directory_connectors; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_connectors ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_connectors sso_directory_connectors_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_connectors_system_access ON public.sso_directory_connectors USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
