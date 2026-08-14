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
-- Name: entity_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.entity_records (
    team_id uuid NOT NULL,
    entity_id uuid DEFAULT gen_random_uuid() NOT NULL,
    entity_kind text NOT NULL,
    identity_context jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entity_records_context_object_check CHECK ((jsonb_typeof(identity_context) = 'object'::text)),
    CONSTRAINT entity_records_kind_check CHECK ((entity_kind = ANY (ARRAY['person'::text, 'organization'::text, 'project'::text, 'product'::text, 'place'::text, 'document'::text, 'concept'::text, 'other'::text]))),
    CONSTRAINT entity_records_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT entity_records_status_check CHECK ((status = ANY (ARRAY['active'::text, 'retired'::text, 'needs_review'::text]))),
    CONSTRAINT entity_records_version_check CHECK ((version >= 1))
);

ALTER TABLE ONLY public.entity_records FORCE ROW LEVEL SECURITY;


--
-- Name: entity_records entity_records_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_records
    ADD CONSTRAINT entity_records_pkey PRIMARY KEY (team_id, entity_id);


--
-- Name: entity_records_team_kind_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX entity_records_team_kind_idx ON public.entity_records USING btree (team_id, entity_kind, created_at DESC);


--
-- Name: entity_records entity_records_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.entity_records
    ADD CONSTRAINT entity_records_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.semantic_team_refs(team_id) ON DELETE RESTRICT;


--
-- Name: entity_records; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.entity_records ENABLE ROW LEVEL SECURITY;

--
-- Name: entity_records entity_records_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_records_insert ON public.entity_records FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_records entity_records_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_records_select ON public.entity_records FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: entity_records entity_records_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY entity_records_update ON public.entity_records FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
