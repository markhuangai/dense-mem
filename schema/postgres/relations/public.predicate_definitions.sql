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
-- Name: predicate_definitions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.predicate_definitions (
    predicate_key text NOT NULL,
    version integer NOT NULL,
    aliases text[] DEFAULT ARRAY[]::text[] NOT NULL,
    allowed_subject_kinds text[] DEFAULT ARRAY[]::text[] NOT NULL,
    allowed_object_kinds text[] DEFAULT ARRAY[]::text[] NOT NULL,
    relationship_kind text NOT NULL,
    current_cardinality text NOT NULL,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT predicate_definitions_current_cardinality_check CHECK ((current_cardinality = ANY (ARRAY['one'::text, 'many'::text]))),
    CONSTRAINT predicate_definitions_key_nonempty CHECK ((btrim(predicate_key) <> ''::text)),
    CONSTRAINT predicate_definitions_lifecycle_state_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'deprecated'::text, 'retired'::text]))),
    CONSTRAINT predicate_definitions_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT predicate_definitions_relationship_kind_check CHECK ((relationship_kind = ANY (ARRAY['state'::text, 'event'::text]))),
    CONSTRAINT predicate_definitions_version_check CHECK ((version >= 1))
);


--
-- Name: predicate_definitions predicate_definitions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.predicate_definitions
    ADD CONSTRAINT predicate_definitions_pkey PRIMARY KEY (predicate_key, version);


--
-- Name: predicate_definitions_aliases_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX predicate_definitions_aliases_idx ON public.predicate_definitions USING gin (aliases);


--
-- Name: predicate_definitions predicate_definitions_reference_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER predicate_definitions_reference_guard BEFORE INSERT OR DELETE OR UPDATE ON public.predicate_definitions FOR EACH ROW EXECUTE FUNCTION public.prevent_reference_definition_mutation();


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
