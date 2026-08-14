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
-- Name: hypothesis_derivation_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hypothesis_derivation_sources (
    team_id uuid NOT NULL,
    derivation_source_id uuid DEFAULT gen_random_uuid() NOT NULL,
    hypothesis_id uuid NOT NULL,
    premise_position smallint NOT NULL,
    relationship_id uuid NOT NULL,
    relationship_version integer NOT NULL,
    support_id uuid,
    observation_id uuid,
    fragment_id uuid NOT NULL,
    source_id uuid,
    source_revision_id uuid,
    source_group_key text DEFAULT ''::text NOT NULL,
    span_start integer NOT NULL,
    span_end integer NOT NULL,
    quote text NOT NULL,
    authority text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT hypothesis_derivation_sources_authority_check CHECK ((authority = ANY (ARRAY['authoritative'::text, 'primary'::text, 'secondary'::text, 'inferred'::text, 'unknown'::text]))),
    CONSTRAINT hypothesis_derivation_sources_group_nonempty CHECK ((btrim(source_group_key) <> ''::text)),
    CONSTRAINT hypothesis_derivation_sources_origin_check CHECK (((support_id IS NULL) <> (observation_id IS NULL))),
    CONSTRAINT hypothesis_derivation_sources_premise_check CHECK ((premise_position = ANY (ARRAY[1, 2]))),
    CONSTRAINT hypothesis_derivation_sources_quote_nonempty CHECK ((btrim(quote) <> ''::text)),
    CONSTRAINT hypothesis_derivation_sources_source_revision_pair_check CHECK (((source_id IS NULL) = (source_revision_id IS NULL))),
    CONSTRAINT hypothesis_derivation_sources_span_check CHECK (((span_start >= 0) AND (span_end > span_start))),
    CONSTRAINT hypothesis_derivation_sources_version_check CHECK ((relationship_version >= 1))
);

ALTER TABLE ONLY public.hypothesis_derivation_sources FORCE ROW LEVEL SECURITY;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_pkey PRIMARY KEY (team_id, derivation_source_id);


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_unique UNIQUE (team_id, hypothesis_id, relationship_id, relationship_version, fragment_id, span_start, span_end);


--
-- Name: hypothesis_derivation_sources_hypothesis_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hypothesis_derivation_sources_hypothesis_idx ON public.hypothesis_derivation_sources USING btree (team_id, hypothesis_id, premise_position, created_at);


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER hypothesis_derivation_sources_append_only BEFORE DELETE OR UPDATE ON public.hypothesis_derivation_sources FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_fragment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_fragment_id_fkey FOREIGN KEY (team_id, fragment_id) REFERENCES public.evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_hypothesis_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_hypothesis_id_fkey FOREIGN KEY (team_id, hypothesis_id) REFERENCES public.hypotheses(team_id, hypothesis_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_observation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_observation_id_fkey FOREIGN KEY (team_id, observation_id) REFERENCES public.relationship_observations(team_id, observation_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_relationship_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_relationship_id_fkey FOREIGN KEY (team_id, relationship_id) REFERENCES public.relationship_records(team_id, relationship_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_source_id_fkey FOREIGN KEY (team_id, source_id) REFERENCES public.evidence_sources(team_id, source_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_source_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_source_revision_id_fkey FOREIGN KEY (team_id, source_revision_id) REFERENCES public.evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_team_id_support_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hypothesis_derivation_sources
    ADD CONSTRAINT hypothesis_derivation_sources_team_id_support_id_fkey FOREIGN KEY (team_id, support_id) REFERENCES public.relationship_evidence_supports(team_id, support_id) ON DELETE RESTRICT;


--
-- Name: hypothesis_derivation_sources; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.hypothesis_derivation_sources ENABLE ROW LEVEL SECURITY;

--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypothesis_derivation_sources_insert ON public.hypothesis_derivation_sources FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: hypothesis_derivation_sources hypothesis_derivation_sources_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY hypothesis_derivation_sources_select ON public.hypothesis_derivation_sources FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
