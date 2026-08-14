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
-- Name: relationship_conflict_positions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_positions (
    team_id uuid NOT NULL,
    conflict_id uuid NOT NULL,
    position_id uuid DEFAULT gen_random_uuid() NOT NULL,
    position_key text NOT NULL,
    object_entity_id uuid,
    object_value_id uuid,
    disposition text DEFAULT 'candidate'::text NOT NULL,
    support_group_count integer DEFAULT 0 NOT NULL,
    authoritative_group_count integer DEFAULT 0 CONSTRAINT relationship_conflict_positi_authoritative_group_count_not_null NOT NULL,
    active boolean DEFAULT true NOT NULL,
    retired_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_conflict_positions_counts_check CHECK (((support_group_count >= 0) AND (authoritative_group_count >= 0))),
    CONSTRAINT relationship_conflict_positions_disposition_check CHECK ((disposition = ANY (ARRAY['candidate'::text, 'preferred'::text, 'suppressed_current'::text]))),
    CONSTRAINT relationship_conflict_positions_key_nonempty CHECK ((btrim(position_key) <> ''::text)),
    CONSTRAINT relationship_conflict_positions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_conflict_positions_object_check CHECK (((object_entity_id IS NULL) <> (object_value_id IS NULL)))
);

ALTER TABLE ONLY public.relationship_conflict_positions FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_positions relationship_conflict_positio_team_id_conflict_id_position__key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positio_team_id_conflict_id_position__key UNIQUE (team_id, conflict_id, position_key);


--
-- Name: relationship_conflict_positions relationship_conflict_positions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positions_pkey PRIMARY KEY (team_id, position_id);


--
-- Name: relationship_conflict_positions_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_positions_case_idx ON public.relationship_conflict_positions USING btree (team_id, conflict_id, disposition, position_id);


--
-- Name: relationship_conflict_positions relationship_conflict_positions_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positions_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_positions relationship_conflict_positions_team_id_object_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positions_team_id_object_entity_id_fkey FOREIGN KEY (team_id, object_entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_positions relationship_conflict_positions_team_id_object_value_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_positions
    ADD CONSTRAINT relationship_conflict_positions_team_id_object_value_id_fkey FOREIGN KEY (team_id, object_value_id) REFERENCES public.value_records(team_id, value_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_positions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_positions ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_positions relationship_conflict_positions_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_positions_insert ON public.relationship_conflict_positions FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_positions relationship_conflict_positions_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_positions_select ON public.relationship_conflict_positions FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_positions relationship_conflict_positions_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_positions_update ON public.relationship_conflict_positions FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
