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
-- Name: identity_external_links; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.identity_external_links (
    identity_id uuid NOT NULL,
    provider text NOT NULL,
    external_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT identity_external_links_external_id_check CHECK (((char_length(external_id) >= 1) AND (char_length(external_id) <= 512))),
    CONSTRAINT identity_external_links_provider_check CHECK (((char_length(provider) >= 1) AND (char_length(provider) <= 128)))
);

ALTER TABLE ONLY public.identity_external_links FORCE ROW LEVEL SECURITY;


--
-- Name: identity_external_links identity_external_links_identity_id_provider_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.identity_external_links
    ADD CONSTRAINT identity_external_links_identity_id_provider_key UNIQUE (identity_id, provider);


--
-- Name: identity_external_links identity_external_links_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.identity_external_links
    ADD CONSTRAINT identity_external_links_pkey PRIMARY KEY (provider, external_id);


--
-- Name: identity_external_links identity_external_links_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.identity_external_links
    ADD CONSTRAINT identity_external_links_identity_id_fkey FOREIGN KEY (identity_id) REFERENCES public.actor_identities(id) ON DELETE CASCADE;


--
-- Name: identity_external_links; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.identity_external_links ENABLE ROW LEVEL SECURITY;

--
-- Name: identity_external_links identity_external_links_context_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY identity_external_links_context_access ON public.identity_external_links USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text]))) WITH CHECK ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- PostgreSQL database dump complete
--
