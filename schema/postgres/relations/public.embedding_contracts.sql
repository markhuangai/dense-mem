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
-- Name: embedding_contracts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.embedding_contracts (
    embedding_contract_id uuid DEFAULT gen_random_uuid() NOT NULL,
    contract_key text NOT NULL,
    version integer NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    dimensions integer NOT NULL,
    distance_metric text DEFAULT 'cosine'::text NOT NULL,
    vector_normalization text DEFAULT 'provider'::text NOT NULL,
    document_format_version integer DEFAULT 1 NOT NULL,
    query_format_version integer DEFAULT 1 NOT NULL,
    lifecycle_state text DEFAULT 'active'::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT embedding_contracts_dimensions_check CHECK (((dimensions >= 1) AND (dimensions <= 16000))),
    CONSTRAINT embedding_contracts_distance_check CHECK ((distance_metric = 'cosine'::text)),
    CONSTRAINT embedding_contracts_format_check CHECK (((document_format_version >= 1) AND (query_format_version >= 1))),
    CONSTRAINT embedding_contracts_key_nonempty CHECK ((btrim(contract_key) <> ''::text)),
    CONSTRAINT embedding_contracts_lifecycle_check CHECK ((lifecycle_state = ANY (ARRAY['active'::text, 'deprecated'::text, 'retired'::text]))),
    CONSTRAINT embedding_contracts_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT embedding_contracts_model_nonempty CHECK ((btrim(model) <> ''::text)),
    CONSTRAINT embedding_contracts_normalization_check CHECK ((vector_normalization = ANY (ARRAY['provider'::text, 'unit'::text, 'none'::text]))),
    CONSTRAINT embedding_contracts_provider_nonempty CHECK ((btrim(provider) <> ''::text)),
    CONSTRAINT embedding_contracts_version_check CHECK ((version >= 1))
);


--
-- Name: embedding_contracts embedding_contracts_contract_key_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_contracts
    ADD CONSTRAINT embedding_contracts_contract_key_version_key UNIQUE (contract_key, version);


--
-- Name: embedding_contracts embedding_contracts_embedding_contract_id_dimensions_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_contracts
    ADD CONSTRAINT embedding_contracts_embedding_contract_id_dimensions_key UNIQUE (embedding_contract_id, dimensions);


--
-- Name: embedding_contracts embedding_contracts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_contracts
    ADD CONSTRAINT embedding_contracts_pkey PRIMARY KEY (embedding_contract_id);


--
-- Name: embedding_contracts embedding_contracts_reference_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER embedding_contracts_reference_guard BEFORE INSERT OR DELETE OR UPDATE ON public.embedding_contracts FOR EACH ROW EXECUTE FUNCTION public.prevent_reference_definition_mutation();


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
