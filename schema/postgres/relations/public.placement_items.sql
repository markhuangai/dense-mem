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
-- Name: placement_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.placement_items (
    team_id uuid NOT NULL,
    placement_item_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_run_id uuid NOT NULL,
    ingest_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    fragment_id uuid NOT NULL,
    evidence_index integer NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    category text DEFAULT 'pending'::text NOT NULL,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    claim_key uuid DEFAULT gen_random_uuid() NOT NULL,
    assessor_attempt_id uuid,
    assessor_attempted_at timestamp with time zone,
    CONSTRAINT placement_items_assessor_attempt_pair_check CHECK (((assessor_attempt_id IS NULL) = (assessor_attempted_at IS NULL))),
    CONSTRAINT placement_items_category_check CHECK ((category = ANY (ARRAY['pending'::text, 'fragment_only'::text, 'candidate'::text, 'validated_claim'::text, 'fact'::text, 'quarantined'::text, 'failed'::text]))),
    CONSTRAINT placement_items_index_check CHECK ((evidence_index >= 0)),
    CONSTRAINT placement_items_result_object_check CHECK ((jsonb_typeof(result) = 'object'::text)),
    CONSTRAINT placement_items_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'awaiting_review'::text, 'completed'::text, 'failed'::text, 'quarantined'::text]))),
    CONSTRAINT placement_items_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.placement_items FORCE ROW LEVEL SECURITY;


--
-- Name: placement_items placement_items_claim_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_claim_ref_unique UNIQUE (team_id, placement_item_id, claim_key);


--
-- Name: placement_items placement_items_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_owner_ref_unique UNIQUE (team_id, placement_item_id, owner_profile_id);


--
-- Name: placement_items placement_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_pkey PRIMARY KEY (team_id, placement_item_id);


--
-- Name: placement_items placement_items_run_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_run_owner_ref_unique UNIQUE (team_id, placement_item_id, placement_run_id, owner_profile_id);


--
-- Name: placement_items placement_items_team_id_placement_run_id_evidence_index_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_placement_run_id_evidence_index_key UNIQUE (team_id, placement_run_id, evidence_index);


--
-- Name: placement_items_migration_ingest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_items_migration_ingest_idx ON public.placement_items USING btree (team_id, ingest_id) WHERE (evidence_index = 0);


--
-- Name: placement_items_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_items_run_idx ON public.placement_items USING btree (team_id, placement_run_id, evidence_index);


--
-- Name: placement_items_team_claim_key_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_items_team_claim_key_unique ON public.placement_items USING btree (team_id, claim_key);


--
-- Name: placement_items placement_items_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_fragment_id_ingest_id_owner_profil_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_fragment_id_ingest_id_owner_profil_fkey FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_ingest_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_ingest_id_owner_profile_id_fkey FOREIGN KEY (team_id, ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_placement_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_placement_run_id_fkey FOREIGN KEY (team_id, placement_run_id) REFERENCES public.placement_runs(team_id, placement_run_id) ON DELETE RESTRICT;


--
-- Name: placement_items placement_items_team_id_placement_run_id_ingest_id_owner_p_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_items
    ADD CONSTRAINT placement_items_team_id_placement_run_id_ingest_id_owner_p_fkey FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_items; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.placement_items ENABLE ROW LEVEL SECURITY;

--
-- Name: placement_items placement_items_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_items_insert ON public.placement_items FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_items placement_items_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_items_select ON public.placement_items FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_items placement_items_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_items_update ON public.placement_items FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
