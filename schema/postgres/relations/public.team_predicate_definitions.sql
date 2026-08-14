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
-- Name: team_predicate_definitions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.team_predicate_definitions (
    team_id uuid NOT NULL,
    predicate_key text NOT NULL,
    version integer NOT NULL,
    aliases text[] DEFAULT ARRAY[]::text[] NOT NULL,
    allowed_subject_kinds text[] DEFAULT ARRAY[]::text[] NOT NULL,
    allowed_object_kinds text[] DEFAULT ARRAY[]::text[] NOT NULL,
    relationship_kind text NOT NULL,
    current_cardinality text NOT NULL,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    origin text DEFAULT 'provider_generated'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT team_predicate_definitions_cardinality_check CHECK ((current_cardinality = ANY (ARRAY['one'::text, 'many'::text]))),
    CONSTRAINT team_predicate_definitions_key_nonempty CHECK ((btrim(predicate_key) <> ''::text)),
    CONSTRAINT team_predicate_definitions_lifecycle_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'deprecated'::text, 'retired'::text]))),
    CONSTRAINT team_predicate_definitions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT team_predicate_definitions_origin_nonempty CHECK ((btrim(origin) <> ''::text)),
    CONSTRAINT team_predicate_definitions_relationship_kind_check CHECK ((relationship_kind = ANY (ARRAY['state'::text, 'event'::text]))),
    CONSTRAINT team_predicate_definitions_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.team_predicate_definitions FORCE ROW LEVEL SECURITY;


--
-- Name: team_predicate_definitions team_predicate_definitions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_predicate_definitions
    ADD CONSTRAINT team_predicate_definitions_pkey PRIMARY KEY (team_id, predicate_key, version);


--
-- Name: team_predicate_definitions_aliases_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX team_predicate_definitions_aliases_idx ON public.team_predicate_definitions USING gin (aliases);


--
-- Name: team_predicate_definitions team_predicate_definitions_reference_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER team_predicate_definitions_reference_guard BEFORE DELETE OR UPDATE ON public.team_predicate_definitions FOR EACH ROW EXECUTE FUNCTION public.prevent_reference_definition_mutation();


--
-- Name: team_predicate_definitions team_predicate_definitions_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.team_predicate_definitions
    ADD CONSTRAINT team_predicate_definitions_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


--
-- Name: team_predicate_definitions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.team_predicate_definitions ENABLE ROW LEVEL SECURITY;

--
-- Name: team_predicate_definitions team_predicate_definitions_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_predicate_definitions_insert ON public.team_predicate_definitions FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: team_predicate_definitions team_predicate_definitions_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY team_predicate_definitions_select ON public.team_predicate_definitions FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
