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
-- Name: dream_path_evaluations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.dream_path_evaluations (
    team_id uuid NOT NULL,
    path_evaluation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    first_relationship_id uuid NOT NULL,
    first_relationship_version integer NOT NULL,
    second_relationship_id uuid NOT NULL,
    second_relationship_version integer NOT NULL,
    provider_model text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    allowed_predicate_fingerprint text NOT NULL,
    CONSTRAINT dream_path_evaluations_distinct_relationships_check CHECK ((first_relationship_id <> second_relationship_id)),
    CONSTRAINT dream_path_evaluations_model_nonempty CHECK ((btrim(provider_model) <> ''::text)),
    CONSTRAINT dream_path_evaluations_predicate_fingerprint_nonempty CHECK ((btrim(allowed_predicate_fingerprint) <> ''::text)),
    CONSTRAINT dream_path_evaluations_versions_check CHECK (((first_relationship_version >= 1) AND (second_relationship_version >= 1)))
);

ALTER TABLE ONLY public.dream_path_evaluations FORCE ROW LEVEL SECURITY;


--
-- Name: dream_path_evaluations dream_path_evaluations_exact_path_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_exact_path_unique UNIQUE (team_id, first_relationship_id, first_relationship_version, second_relationship_id, second_relationship_version, allowed_predicate_fingerprint);


--
-- Name: dream_path_evaluations dream_path_evaluations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_pkey PRIMARY KEY (team_id, path_evaluation_id);


--
-- Name: dream_path_evaluations_first_relationship_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX dream_path_evaluations_first_relationship_idx ON public.dream_path_evaluations USING btree (team_id, first_relationship_id, first_relationship_version);


--
-- Name: dream_path_evaluations dream_path_evaluations_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER dream_path_evaluations_append_only BEFORE DELETE OR UPDATE ON public.dream_path_evaluations FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: dream_path_evaluations dream_path_evaluations_team_id_first_relationship_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_team_id_first_relationship_id_fkey FOREIGN KEY (team_id, first_relationship_id) REFERENCES public.relationship_records(team_id, relationship_id) ON DELETE RESTRICT;


--
-- Name: dream_path_evaluations dream_path_evaluations_team_id_second_relationship_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_team_id_second_relationship_id_fkey FOREIGN KEY (team_id, second_relationship_id) REFERENCES public.relationship_records(team_id, relationship_id) ON DELETE RESTRICT;


--
-- Name: dream_path_evaluations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.dream_path_evaluations ENABLE ROW LEVEL SECURITY;

--
-- Name: dream_path_evaluations dream_path_evaluations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_path_evaluations_insert ON public.dream_path_evaluations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: dream_path_evaluations dream_path_evaluations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY dream_path_evaluations_select ON public.dream_path_evaluations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
