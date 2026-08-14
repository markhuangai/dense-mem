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
-- Name: entity_names; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_names (
    team_id uuid NOT NULL,
    entity_name_id uuid DEFAULT gen_random_uuid() NOT NULL,
    entity_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    display_name text NOT NULL,
    normalized_name text NOT NULL,
    name_kind text NOT NULL,
    locale text DEFAULT ''::text NOT NULL,
    valid_from timestamp with time zone,
    valid_to timestamp with time zone,
    fragment_id uuid,
    span_start integer,
    span_end integer,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entity_names_display_nonempty CHECK ((btrim(display_name) <> ''::text)),
    CONSTRAINT entity_names_kind_check CHECK ((name_kind = ANY (ARRAY['canonical'::text, 'alias'::text, 'former'::text]))),
    CONSTRAINT entity_names_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT entity_names_normalized_nonempty CHECK ((btrim(normalized_name) <> ''::text)),
    CONSTRAINT entity_names_span_check CHECK ((((span_start IS NULL) AND (span_end IS NULL)) OR ((span_start IS NOT NULL) AND (span_end IS NOT NULL) AND (span_start >= 0) AND (span_end > span_start)))),
    CONSTRAINT entity_names_valid_window_check CHECK (((valid_to IS NULL) OR (valid_from IS NULL) OR (valid_to >= valid_from)))
);

ALTER TABLE ONLY public.entity_names FORCE ROW LEVEL SECURITY;


--
-- Name: entity_names entity_names_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_pkey PRIMARY KEY (team_id, entity_name_id);


--
-- Name: entity_names_current_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX entity_names_current_canonical_unique ON public.entity_names USING btree (team_id, entity_id) WHERE ((name_kind = 'canonical'::text) AND (valid_to IS NULL));


--
-- Name: entity_names_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX entity_names_lookup_idx ON public.entity_names USING btree (team_id, normalized_name, name_kind, entity_id);


--
-- Name: entity_names entity_names_team_id_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_team_id_entity_id_fkey FOREIGN KEY (team_id, entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: entity_names entity_names_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: entity_names entity_names_team_id_fragment_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_team_id_fragment_id_owner_profile_id_fkey FOREIGN KEY (team_id, fragment_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: entity_names entity_names_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_names
    ADD CONSTRAINT entity_names_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: entity_names; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_names ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_names entity_names_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_names_insert ON public.entity_names FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_names entity_names_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_names_select ON public.entity_names FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_names entity_names_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_names_update ON public.entity_names FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
