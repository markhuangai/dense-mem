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
-- Name: relationship_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_records (
    team_id uuid NOT NULL,
    relationship_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    semantic_group_key text NOT NULL,
    subject_entity_id uuid NOT NULL,
    predicate_key text NOT NULL,
    predicate_version integer NOT NULL,
    object_entity_id uuid,
    object_value_id uuid,
    relationship_kind text NOT NULL,
    current_cardinality text NOT NULL,
    status text NOT NULL,
    polarity text DEFAULT '+'::text NOT NULL,
    scope_key text,
    valid_from timestamp with time zone,
    valid_to timestamp with time zone,
    support_count integer DEFAULT 0 NOT NULL,
    source_group_count integer DEFAULT 0 NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    recorded_to timestamp with time zone,
    identity_alias_of_relationship_id uuid,
    CONSTRAINT relationship_records_cardinality_check CHECK ((current_cardinality = ANY (ARRAY['one'::text, 'many'::text]))),
    CONSTRAINT relationship_records_counts_check CHECK (((support_count >= 0) AND (source_group_count >= 0))),
    CONSTRAINT relationship_records_identity_alias_not_self_check CHECK (((identity_alias_of_relationship_id IS NULL) OR (identity_alias_of_relationship_id <> relationship_id))),
    CONSTRAINT relationship_records_kind_check CHECK ((relationship_kind = ANY (ARRAY['state'::text, 'event'::text]))),
    CONSTRAINT relationship_records_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_records_object_check CHECK (((object_entity_id IS NULL) <> (object_value_id IS NULL))),
    CONSTRAINT relationship_records_polarity_check CHECK ((polarity = ANY (ARRAY['+'::text, '-'::text]))),
    CONSTRAINT relationship_records_semantic_group_nonempty CHECK ((btrim(semantic_group_key) <> ''::text)),
    CONSTRAINT relationship_records_status_check CHECK ((status = ANY (ARRAY['pending_evidence'::text, 'active'::text, 'needs_review'::text, 'quarantined'::text, 'superseded'::text, 'disputed'::text, 'retracted'::text, 'rejected'::text]))),
    CONSTRAINT relationship_records_valid_window_check CHECK (((valid_to IS NULL) OR (valid_from IS NULL) OR (valid_to >= valid_from))),
    CONSTRAINT relationship_records_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.relationship_records FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_records relationship_records_owner_fk_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_owner_fk_unique UNIQUE (team_id, relationship_id, owner_profile_id);


--
-- Name: relationship_records relationship_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_pkey PRIMARY KEY (team_id, relationship_id);


--
-- Name: relationship_records_active_object_entity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_active_object_entity_idx ON public.relationship_records USING btree (team_id, object_entity_id, predicate_key) WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0) AND (object_entity_id IS NOT NULL));


--
-- Name: relationship_records_active_object_value_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_active_object_value_idx ON public.relationship_records USING btree (team_id, object_value_id, predicate_key) WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0) AND (object_value_id IS NOT NULL));


--
-- Name: relationship_records_active_one_current_canonical_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_records_active_one_current_canonical_unique ON public.relationship_records USING btree (team_id, owner_profile_id, subject_entity_id, predicate_key, polarity, valid_from, scope_key) NULLS NOT DISTINCT WHERE ((identity_alias_of_relationship_id IS NULL) AND (current_cardinality = 'one'::text) AND (status = 'active'::text) AND (support_count > 0));


--
-- Name: relationship_records_active_subject_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_active_subject_idx ON public.relationship_records USING btree (team_id, subject_entity_id, predicate_key) WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0));


--
-- Name: relationship_records_active_supported_team_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_active_supported_team_idx ON public.relationship_records USING btree (team_id) WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0));


--
-- Name: relationship_records_canonical_identity_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_records_canonical_identity_unique ON public.relationship_records USING btree (team_id, owner_profile_id, subject_entity_id, predicate_key, object_entity_id, object_value_id, polarity, valid_from, scope_key) NULLS NOT DISTINCT WHERE (identity_alias_of_relationship_id IS NULL);


--
-- Name: relationship_records_identity_alias_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_records_identity_alias_idx ON public.relationship_records USING btree (team_id, identity_alias_of_relationship_id) WHERE (identity_alias_of_relationship_id IS NOT NULL);


--
-- Name: relationship_records relationship_records_identity_alias_owner_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_identity_alias_owner_fk FOREIGN KEY (team_id, identity_alias_of_relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_id_object_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_id_object_entity_id_fkey FOREIGN KEY (team_id, object_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_id_object_value_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_id_object_value_id_fkey FOREIGN KEY (team_id, object_value_id) REFERENCES public.value_records(team_id, value_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_id_subject_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_id_subject_entity_id_fkey FOREIGN KEY (team_id, subject_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_records relationship_records_team_predicate_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_records
    ADD CONSTRAINT relationship_records_team_predicate_fkey FOREIGN KEY (team_id, predicate_key, predicate_version) REFERENCES public.team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT;


--
-- Name: relationship_records; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_records ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_records relationship_records_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_records_insert ON public.relationship_records FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_records relationship_records_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_records_select ON public.relationship_records FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_records relationship_records_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_records_update ON public.relationship_records FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
