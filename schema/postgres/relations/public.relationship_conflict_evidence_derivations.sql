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
-- Name: relationship_conflict_evidence_derivations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_conflict_evidence_derivations (
    team_id uuid NOT NULL,
    derivation_id uuid DEFAULT gen_random_uuid() CONSTRAINT relationship_conflict_evidence_derivatio_derivation_id_not_null NOT NULL,
    conflict_id uuid NOT NULL,
    target_fragment_id uuid CONSTRAINT relationship_conflict_evidence_deri_target_fragment_id_not_null NOT NULL,
    target_owner_profile_id uuid CONSTRAINT relationship_conflict_evidence_target_owner_profile_id_not_null NOT NULL,
    selected_position_id uuid CONSTRAINT relationship_conflict_evidence_de_selected_position_id_not_null NOT NULL,
    replacement_fragment_id uuid,
    system_profile_id uuid CONSTRAINT relationship_conflict_evidence_deriv_system_profile_id_not_null NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidenc_team_id_conflict_id_target_fr_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidenc_team_id_conflict_id_target_fr_key UNIQUE (team_id, conflict_id, target_fragment_id);


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidence_derivations_pkey PRIMARY KEY (team_id, derivation_id);


--
-- Name: relationship_conflict_evidence_derivations_conflict_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_conflict_evidence_derivations_conflict_idx ON public.relationship_conflict_evidence_derivations USING btree (team_id, conflict_id, created_at);


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivations_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_conflict_evidence_derivations_append_only BEFORE DELETE OR UPDATE ON public.relationship_conflict_evidence_derivations FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidenc_team_id_replacement_fragment_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidenc_team_id_replacement_fragment_fkey FOREIGN KEY (team_id, replacement_fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidenc_team_id_selected_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidenc_team_id_selected_position_id_fkey FOREIGN KEY (team_id, selected_position_id) REFERENCES public.relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidenc_team_id_target_fragment_id_t_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidenc_team_id_target_fragment_id_t_fkey FOREIGN KEY (team_id, target_fragment_id, target_owner_profile_id) REFERENCES public.evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_d_team_id_system_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidence_d_team_id_system_profile_id_fkey FOREIGN KEY (team_id, system_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivat_team_id_conflict_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_conflict_evidence_derivations
    ADD CONSTRAINT relationship_conflict_evidence_derivat_team_id_conflict_id_fkey FOREIGN KEY (team_id, conflict_id) REFERENCES public.relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT;


--
-- Name: relationship_conflict_evidence_derivations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_conflict_evidence_derivations ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_evidence_derivations_insert ON public.relationship_conflict_evidence_derivations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_conflict_evidence_derivations relationship_conflict_evidence_derivations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_conflict_evidence_derivations_select ON public.relationship_conflict_evidence_derivations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
