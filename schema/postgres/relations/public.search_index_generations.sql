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
-- Name: search_index_generations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.search_index_generations (
    search_index_generation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    generation integer NOT NULL,
    embedding_contract_id uuid NOT NULL,
    embedding_dimensions integer NOT NULL,
    ann_strategy text DEFAULT 'exact'::text NOT NULL,
    operator_class text DEFAULT ''::text NOT NULL,
    indexed_expression text DEFAULT ''::text NOT NULL,
    physical_index_name text DEFAULT ''::text NOT NULL,
    hnsw_m integer DEFAULT 16 NOT NULL,
    hnsw_ef_construction integer DEFAULT 64 NOT NULL,
    query_ef_search integer DEFAULT 40 NOT NULL,
    exact_max_rows integer DEFAULT 10000 NOT NULL,
    candidate_limit integer DEFAULT 200 NOT NULL,
    allow_exact_fallback boolean DEFAULT false NOT NULL,
    activation_state text DEFAULT 'building'::text NOT NULL,
    failure_reason text DEFAULT ''::text NOT NULL,
    activated_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT search_index_generations_active_time_check CHECK ((((activation_state = 'active'::text) AND (activated_at IS NOT NULL)) OR (activation_state <> 'active'::text))),
    CONSTRAINT search_index_generations_dimension_strategy_check CHECK (((ann_strategy = 'exact'::text) OR ((ann_strategy = 'vector_hnsw'::text) AND ((embedding_dimensions >= 1) AND (embedding_dimensions <= 2000))) OR ((ann_strategy = 'halfvec_hnsw'::text) AND ((embedding_dimensions >= 1) AND (embedding_dimensions <= 4000))) OR ((ann_strategy = 'binary_hnsw'::text) AND ((embedding_dimensions >= 1) AND (embedding_dimensions <= 16000))))),
    CONSTRAINT search_index_generations_generation_check CHECK ((generation >= 1)),
    CONSTRAINT search_index_generations_hnsw_index_check CHECK (((ann_strategy = 'exact'::text) OR ((btrim(physical_index_name) <> ''::text) AND (btrim(operator_class) <> ''::text) AND (btrim(indexed_expression) <> ''::text)))),
    CONSTRAINT search_index_generations_hnsw_positive CHECK (((hnsw_m > 0) AND (hnsw_ef_construction > 0) AND (query_ef_search > 0) AND (exact_max_rows > 0) AND (candidate_limit > 0))),
    CONSTRAINT search_index_generations_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT search_index_generations_operator_class_check CHECK (((ann_strategy = 'exact'::text) OR ((ann_strategy = 'vector_hnsw'::text) AND (operator_class = 'vector_cosine_ops'::text)) OR ((ann_strategy = 'halfvec_hnsw'::text) AND (operator_class = 'halfvec_cosine_ops'::text)) OR ((ann_strategy = 'binary_hnsw'::text) AND (operator_class = 'bit_hamming_ops'::text)))),
    CONSTRAINT search_index_generations_state_check CHECK ((activation_state = ANY (ARRAY['building'::text, 'active'::text, 'failed'::text, 'deprecated'::text, 'retired'::text]))),
    CONSTRAINT search_index_generations_strategy_check CHECK ((ann_strategy = ANY (ARRAY['exact'::text, 'vector_hnsw'::text, 'halfvec_hnsw'::text, 'binary_hnsw'::text])))
);


--
-- Name: search_index_generations search_index_generations_embedding_contract_id_generation_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_index_generations
    ADD CONSTRAINT search_index_generations_embedding_contract_id_generation_key UNIQUE (embedding_contract_id, generation);


--
-- Name: search_index_generations search_index_generations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_index_generations
    ADD CONSTRAINT search_index_generations_pkey PRIMARY KEY (search_index_generation_id);


--
-- Name: search_index_generations search_index_generations_search_index_generation_id_embeddi_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_index_generations
    ADD CONSTRAINT search_index_generations_search_index_generation_id_embeddi_key UNIQUE (search_index_generation_id, embedding_contract_id);


--
-- Name: search_index_generations search_index_generations_lifecycle_guard; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER search_index_generations_lifecycle_guard BEFORE INSERT OR DELETE OR UPDATE ON public.search_index_generations FOR EACH ROW EXECUTE FUNCTION public.guard_search_index_generation_lifecycle();


--
-- Name: search_index_generations search_index_generations_embedding_contract_id_embedding_d_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_index_generations
    ADD CONSTRAINT search_index_generations_embedding_contract_id_embedding_d_fkey FOREIGN KEY (embedding_contract_id, embedding_dimensions) REFERENCES public.embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT;


--
-- PostgreSQL database dump complete
--
