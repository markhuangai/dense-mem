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
-- Name: evidence_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_sources (
    team_id uuid NOT NULL,
    source_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_key text NOT NULL,
    source_kind text DEFAULT 'conversation'::text NOT NULL,
    authority text DEFAULT 'primary'::text NOT NULL,
    current_revision_id uuid,
    current_revision_token text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_sources_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT evidence_sources_key_nonempty CHECK ((btrim(source_key) <> ''::text)),
    CONSTRAINT evidence_sources_kind_check CHECK ((source_kind = ANY (ARRAY['conversation'::text, 'document'::text, 'integration'::text, 'manual'::text]))),
    CONSTRAINT evidence_sources_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.evidence_sources FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_sources evidence_sources_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_sources
    ADD CONSTRAINT evidence_sources_owner_ref_unique UNIQUE (team_id, source_id, owner_profile_id);


--
-- Name: evidence_sources evidence_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_sources
    ADD CONSTRAINT evidence_sources_pkey PRIMARY KEY (team_id, source_id);


--
-- Name: evidence_sources_owner_key_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX evidence_sources_owner_key_unique ON public.evidence_sources USING btree (team_id, owner_profile_id, source_key);


--
-- Name: evidence_sources evidence_sources_current_revision_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_sources
    ADD CONSTRAINT evidence_sources_current_revision_fk FOREIGN KEY (team_id, source_id, current_revision_id, owner_profile_id) REFERENCES public.evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT DEFERRABLE;


--
-- Name: evidence_sources evidence_sources_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_sources
    ADD CONSTRAINT evidence_sources_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_sources; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_sources ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_sources evidence_sources_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_sources_insert ON public.evidence_sources FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_sources evidence_sources_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_sources_select ON public.evidence_sources FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_sources evidence_sources_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_sources_update ON public.evidence_sources FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
