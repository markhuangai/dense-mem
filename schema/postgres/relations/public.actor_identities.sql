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
-- Name: actor_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.actor_identities (
    id uuid NOT NULL,
    kind text NOT NULL,
    team_id uuid,
    provider text DEFAULT ''::text NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT actor_identities_display_name_check CHECK ((char_length(display_name) <= 512)),
    CONSTRAINT actor_identities_kind_check CHECK ((kind = ANY (ARRAY['human'::text, 'api_client'::text, 'system'::text]))),
    CONSTRAINT actor_identities_provider_check CHECK ((char_length(provider) <= 128)),
    CONSTRAINT actor_identities_subject_check CHECK ((char_length(subject) <= 512))
);

ALTER TABLE ONLY public.actor_identities FORCE ROW LEVEL SECURITY;


--
-- Name: actor_identities actor_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_identities
    ADD CONSTRAINT actor_identities_pkey PRIMARY KEY (id);


--
-- Name: idx_actor_identities_provider_subject; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_actor_identities_provider_subject ON public.actor_identities USING btree (provider, subject) WHERE ((provider <> ''::text) AND (subject <> ''::text));


--
-- Name: idx_actor_identities_team_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_actor_identities_team_active ON public.actor_identities USING btree (team_id, active);


--
-- Name: actor_identities actor_identities_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.actor_identities
    ADD CONSTRAINT actor_identities_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: actor_identities; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.actor_identities ENABLE ROW LEVEL SECURITY;

--
-- Name: actor_identities actor_identities_context_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY actor_identities_context_access ON public.actor_identities USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)));


--
-- PostgreSQL database dump complete
--
