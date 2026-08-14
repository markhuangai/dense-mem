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
-- Name: sso_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(100) NOT NULL,
    kind character varying(32) NOT NULL,
    issuer_url text NOT NULL,
    client_id text NOT NULL,
    client_secret_env text DEFAULT ''::text NOT NULL,
    scopes text[] DEFAULT ARRAY['openid'::text, 'profile'::text, 'email'::text] NOT NULL,
    group_claims text[] DEFAULT ARRAY['groups'::text] NOT NULL,
    groups_endpoint text DEFAULT ''::text NOT NULL,
    groups_scopes text[] DEFAULT ARRAY[]::text[] NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text DEFAULT ''::text NOT NULL,
    identity_claim text DEFAULT ''::text NOT NULL,
    retired_at timestamp with time zone,
    CONSTRAINT sso_providers_kind_check CHECK (((kind)::text = ANY ((ARRAY['azure_ad'::character varying, 'pingone'::character varying, 'generic_oidc'::character varying])::text[])))
);

ALTER TABLE ONLY public.sso_providers FORCE ROW LEVEL SECURITY;


--
-- Name: sso_providers sso_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_providers
    ADD CONSTRAINT sso_providers_pkey PRIMARY KEY (id);


--
-- Name: idx_sso_providers_name_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_providers_name_unique ON public.sso_providers USING btree (lower((name)::text));


--
-- Name: sso_providers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_providers ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_providers sso_providers_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_providers_system_access ON public.sso_providers USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
