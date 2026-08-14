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
-- Name: sso_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    subject text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    last_login_at timestamp with time zone,
    last_entitlement_check_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    external_id text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL
);

ALTER TABLE ONLY public.sso_identities FORCE ROW LEVEL SECURITY;


--
-- Name: sso_identities sso_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_identities
    ADD CONSTRAINT sso_identities_pkey PRIMARY KEY (id);


--
-- Name: sso_identities sso_identities_provider_id_subject_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_identities
    ADD CONSTRAINT sso_identities_provider_id_subject_key UNIQUE (provider_id, subject);


--
-- Name: idx_sso_identities_provider_external_id_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_sso_identities_provider_external_id_unique ON public.sso_identities USING btree (provider_id, external_id) WHERE (external_id <> ''::text);


--
-- Name: idx_sso_identities_provider_subject; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_identities_provider_subject ON public.sso_identities USING btree (provider_id, subject);


--
-- Name: sso_identities sso_identities_identity_bridge; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER sso_identities_identity_bridge AFTER INSERT OR DELETE OR UPDATE OF provider_id, subject, external_id, display_name, active ON public.sso_identities FOR EACH ROW EXECUTE FUNCTION public.dense_mem_sync_sso_identity();


--
-- Name: sso_identities sso_identities_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_identities
    ADD CONSTRAINT sso_identities_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_identities; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_identities ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_identities sso_identities_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_identities_system_access ON public.sso_identities USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
