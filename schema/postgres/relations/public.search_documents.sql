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
-- Name: search_documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.search_documents (
    team_id uuid NOT NULL,
    search_document_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    source_kind text NOT NULL,
    source_id uuid NOT NULL,
    source_version bigint NOT NULL,
    document_version bigint DEFAULT 1 NOT NULL,
    embedding_contract_id uuid NOT NULL,
    embedding_dimensions integer NOT NULL,
    search_state text DEFAULT 'pending'::text NOT NULL,
    document_text text NOT NULL,
    document_hash text NOT NULL,
    search_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, document_text)) STORED,
    embedding public.vector,
    embedding_updated_at timestamp with time zone,
    embedding_error text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    projection_format_version integer DEFAULT 1 NOT NULL,
    projection_generation_id uuid,
    CONSTRAINT search_documents_document_version_check CHECK ((document_version >= 1)),
    CONSTRAINT search_documents_embedding_dims_check CHECK (((embedding IS NULL) OR (public.vector_dims(embedding) = embedding_dimensions))),
    CONSTRAINT search_documents_hash_nonempty CHECK ((btrim(document_hash) <> ''::text)),
    CONSTRAINT search_documents_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT search_documents_projection_format_check CHECK ((projection_format_version >= 1)),
    CONSTRAINT search_documents_source_kind_check CHECK ((source_kind = ANY (ARRAY['evidence'::text, 'relationship'::text, 'entity'::text]))),
    CONSTRAINT search_documents_source_version_check CHECK ((source_version >= 1)),
    CONSTRAINT search_documents_state_check CHECK ((search_state = ANY (ARRAY['not_required'::text, 'pending'::text, 'current'::text, 'failed'::text]))),
    CONSTRAINT search_documents_text_nonempty CHECK ((btrim(document_text) <> ''::text))
);

ALTER TABLE ONLY public.search_documents FORCE ROW LEVEL SECURITY;


--
-- Name: search_documents search_documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_pkey PRIMARY KEY (team_id, search_document_id);


--
-- Name: search_documents search_documents_team_id_source_kind_source_id_embedding_co_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_team_id_source_kind_source_id_embedding_co_key UNIQUE (team_id, source_kind, source_id, embedding_contract_id);


--
-- Name: search_documents_contract_state_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_contract_state_idx ON public.search_documents USING btree (team_id, embedding_contract_id, search_state, source_kind);


--
-- Name: search_documents_fts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_fts_idx ON public.search_documents USING gin (search_tsv);


--
-- Name: search_documents_relationship_projection_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_relationship_projection_idx ON public.search_documents USING btree (team_id, source_kind, projection_format_version, projection_generation_id, search_state) WHERE (source_kind = 'relationship'::text);


--
-- Name: search_documents_source_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_source_idx ON public.search_documents USING btree (team_id, source_kind, source_id, source_version DESC);


--
-- Name: search_documents_state_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX search_documents_state_idx ON public.search_documents USING btree (team_id, search_state, updated_at DESC, search_document_id);


--
-- Name: search_documents search_documents_embedding_contract_id_embedding_dimension_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_embedding_contract_id_embedding_dimension_fkey FOREIGN KEY (embedding_contract_id, embedding_dimensions) REFERENCES public.embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT;


--
-- Name: search_documents search_documents_projection_generation_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_projection_generation_fk FOREIGN KEY (team_id, projection_generation_id) REFERENCES public.search_projection_generations(team_id, projection_generation_id) ON DELETE RESTRICT;


--
-- Name: search_documents search_documents_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.search_documents
    ADD CONSTRAINT search_documents_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: search_documents; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.search_documents ENABLE ROW LEVEL SECURITY;

--
-- Name: search_documents search_documents_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_documents_insert ON public.search_documents FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: search_documents search_documents_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_documents_select ON public.search_documents FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: search_documents search_documents_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY search_documents_update ON public.search_documents FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
