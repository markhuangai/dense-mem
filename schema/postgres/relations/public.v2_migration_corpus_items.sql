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
-- Name: v2_migration_corpus_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_corpus_items (
    item_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    team_id uuid NOT NULL,
    owner_profile_id uuid,
    source_kind text DEFAULT 'neo4j'::text NOT NULL,
    source_id text NOT NULL,
    source_hash text DEFAULT ''::text NOT NULL,
    item_kind text NOT NULL,
    outcome text DEFAULT 'pending'::text NOT NULL,
    ingest_id uuid,
    placement_item_id uuid,
    exclusion_reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_corpus_items_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT v2_migration_corpus_items_outcome_check CHECK ((outcome = ANY (ARRAY['pending'::text, 'accepted'::text, 'needs_review'::text, 'rejected'::text, 'quarantined'::text, 'failed'::text, 'excluded'::text])))
);

ALTER TABLE ONLY public.v2_migration_corpus_items FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_pkey PRIMARY KEY (item_id);


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_run_id_source_kind_source_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_run_id_source_kind_source_id_key UNIQUE (run_id, source_kind, source_id);


--
-- Name: idx_v2_migration_corpus_run_outcome; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_corpus_run_outcome ON public.v2_migration_corpus_items USING btree (run_id, outcome, updated_at DESC);


--
-- Name: idx_v2_migration_corpus_team_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_corpus_team_owner ON public.v2_migration_corpus_items USING btree (run_id, team_id, owner_profile_id, outcome);


--
-- Name: v2_migration_corpus_run_ingest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX v2_migration_corpus_run_ingest_idx ON public.v2_migration_corpus_items USING btree (run_id, team_id, ingest_id) WHERE (ingest_id IS NOT NULL);


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE CASCADE;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_team_id_ingest_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_team_id_ingest_id_fkey FOREIGN KEY (team_id, ingest_id) REFERENCES public.knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.team_profiles(team_id, id) ON DELETE RESTRICT;


--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_team_id_placement_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_corpus_items
    ADD CONSTRAINT v2_migration_corpus_items_team_id_placement_item_id_fkey FOREIGN KEY (team_id, placement_item_id) REFERENCES public.placement_items(team_id, placement_item_id) ON DELETE RESTRICT;


--
-- Name: v2_migration_corpus_items; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_corpus_items ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_corpus_items v2_migration_corpus_items_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_corpus_items_system_select ON public.v2_migration_corpus_items FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
