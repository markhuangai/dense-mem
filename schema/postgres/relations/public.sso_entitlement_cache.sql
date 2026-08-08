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
-- Name: sso_entitlement_cache; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_entitlement_cache (
    provider_id uuid NOT NULL,
    subject text NOT NULL,
    groups text[] DEFAULT ARRAY[]::text[] NOT NULL,
    status character varying(20) NOT NULL,
    checked_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    CONSTRAINT sso_entitlement_cache_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('denied'::character varying)::text, ('error'::character varying)::text])))
);

ALTER TABLE ONLY public.sso_entitlement_cache FORCE ROW LEVEL SECURITY;


--
-- Name: sso_entitlement_cache sso_entitlement_cache_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_entitlement_cache
    ADD CONSTRAINT sso_entitlement_cache_pkey PRIMARY KEY (provider_id, subject);


--
-- Name: sso_entitlement_cache sso_entitlement_cache_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_entitlement_cache
    ADD CONSTRAINT sso_entitlement_cache_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.sso_providers(id) ON DELETE CASCADE;


--
-- Name: sso_entitlement_cache; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_entitlement_cache ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_entitlement_cache sso_entitlement_cache_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_entitlement_cache_system_access ON public.sso_entitlement_cache USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
