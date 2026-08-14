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
-- Name: evidence_lifecycle_operations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_lifecycle_operations (
    team_id uuid NOT NULL,
    lifecycle_operation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    action text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    replacement_ingest_id uuid,
    result jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    actor_profile_id uuid,
    CONSTRAINT evidence_lifecycle_operations_action_check CHECK ((action = ANY (ARRAY['supersede'::text, 'retract'::text]))),
    CONSTRAINT evidence_lifecycle_operations_idempotency_nonempty CHECK ((btrim(idempotency_key) <> ''::text)),
    CONSTRAINT evidence_lifecycle_operations_reason_length_check CHECK ((char_length(reason) <= 1000)),
    CONSTRAINT evidence_lifecycle_operations_request_hash_nonempty CHECK ((btrim(request_hash) <> ''::text)),
    CONSTRAINT evidence_lifecycle_operations_result_object_check CHECK ((jsonb_typeof(result) = 'object'::text))
);

ALTER TABLE ONLY public.evidence_lifecycle_operations FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_owner_ref_unique UNIQUE (team_id, lifecycle_operation_id, owner_profile_id);


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_pkey PRIMARY KEY (team_id, lifecycle_operation_id);


--
-- Name: evidence_lifecycle_operations_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX evidence_lifecycle_operations_idempotency_unique ON public.evidence_lifecycle_operations USING btree (team_id, owner_profile_id, idempotency_key);


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_lifecycle_operations_append_only BEFORE DELETE OR UPDATE ON public.evidence_lifecycle_operations FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_actor_profile_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_actor_profile_fk FOREIGN KEY (team_id, actor_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_team_id_replacement_ingest_i_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_team_id_replacement_ingest_i_fkey FOREIGN KEY (team_id, replacement_ingest_id, owner_profile_id) REFERENCES public.knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_lifecycle_operations; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_lifecycle_operations ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_lifecycle_operations_insert ON public.evidence_lifecycle_operations FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_lifecycle_operations evidence_lifecycle_operations_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_lifecycle_operations_select ON public.evidence_lifecycle_operations FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
