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
-- Name: relationship_conflict_position_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_position_members (
    team_id uuid NOT NULL,
    conflict_id uuid NOT NULL,
    position_id uuid NOT NULL,
    relationship_id uuid NOT NULL,
    owner_profile_id uuid CONSTRAINT relationship_conflict_position_member_owner_profile_id_not_null NOT NULL,
    support_id uuid,
    verification_event_id uuid,
    fragment_id uuid,
    source_group_key text CONSTRAINT relationship_conflict_position_member_source_group_key_not_null NOT NULL,
    authority text DEFAULT 'primary'::text NOT NULL,
    effective_at timestamp with time zone,
    effective_time_basis text DEFAULT ''::text CONSTRAINT relationship_conflict_position_me_effective_time_basis_not_null NOT NULL,
    recorded_fallback boolean DEFAULT false CONSTRAINT relationship_conflict_position_membe_recorded_fallback_not_null NOT NULL,
    active boolean DEFAULT true NOT NULL,
    retired_at timestamp with time zone,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    accepted_at timestamp with time zone NOT NULL,
    CONSTRAINT relationship_conflict_members_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT relationship_conflict_members_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT relationship_conflict_members_source_group_nonempty CHECK ((btrim(source_group_key) <> ''::text))
);

ALTER TABLE ONLY public.relationship_conflict_position_members FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_position_members_pkey PRIMARY KEY (team_id, position_id, relationship_id, source_group_key);


--
-- Name: relationship_conflict_members_case_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_members_case_idx ON public.relationship_conflict_position_members USING btree (team_id, conflict_id, relationship_id);


--
-- Name: relationship_conflict_members_relationship_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_members_relationship_idx ON public.relationship_conflict_position_members USING btree (team_id, relationship_id);


--
-- Name: relationship_conflict_position_members relationship_conflict_positio_team_id_relationship_id_owne_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_positio_team_id_relationship_id_owne_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_positio_team_id_support_id_owner_pro_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_positio_team_id_support_id_owner_pro_fkey FOREIGN KEY (team_id, support_id, owner_profile_id) REFERENCES public.relationship_evidence_supports(team_id, support_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_positio_team_id_verification_event_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_positio_team_id_verification_event_i_fkey FOREIGN KEY (team_id, verification_event_id, owner_profile_id) REFERENCES public.verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_position_members_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_position_members_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_team_id_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_position_members
    ADD CONSTRAINT relationship_conflict_position_members_team_id_position_id_fkey FOREIGN KEY (team_id, position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_position_members; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_position_members ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_position_members_insert ON public.relationship_conflict_position_members FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_position_members_select ON public.relationship_conflict_position_members FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_position_members relationship_conflict_position_members_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_position_members_update ON public.relationship_conflict_position_members FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
