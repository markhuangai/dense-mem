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
-- Name: placement_assessments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.placement_assessments (
    team_id uuid NOT NULL,
    assessment_id uuid DEFAULT gen_random_uuid() NOT NULL,
    placement_item_id uuid,
    claim_key uuid,
    owner_profile_id uuid NOT NULL,
    request_id text NOT NULL,
    assessor_contract_version text NOT NULL,
    model text NOT NULL,
    tokenizer text NOT NULL,
    input_tokens integer NOT NULL,
    output_tokens integer NOT NULL,
    candidate_context_tokens integer NOT NULL,
    candidate_context_truncated boolean DEFAULT false NOT NULL,
    normalized_response jsonb NOT NULL,
    response_hash text NOT NULL,
    validated_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    assessment_scope text DEFAULT 'item'::text NOT NULL,
    placement_run_id uuid,
    ingest_id uuid,
    CONSTRAINT placement_assessments_contract_nonempty CHECK ((btrim(assessor_contract_version) <> ''::text)),
    CONSTRAINT placement_assessments_model_nonempty CHECK ((btrim(model) <> ''::text)),
    CONSTRAINT placement_assessments_request_nonempty CHECK ((btrim(request_id) <> ''::text)),
    CONSTRAINT placement_assessments_response_hash_nonempty CHECK ((btrim(response_hash) <> ''::text)),
    CONSTRAINT placement_assessments_response_object_check CHECK ((jsonb_typeof(normalized_response) = 'object'::text)),
    CONSTRAINT placement_assessments_scope_shape_check CHECK ((((assessment_scope = 'item'::text) AND (placement_item_id IS NOT NULL) AND (claim_key IS NOT NULL) AND (placement_run_id IS NULL) AND (ingest_id IS NULL)) OR ((assessment_scope = 'submission'::text) AND (placement_item_id IS NULL) AND (claim_key IS NULL) AND (placement_run_id IS NOT NULL) AND (ingest_id IS NOT NULL)))),
    CONSTRAINT placement_assessments_token_counts_check CHECK (((input_tokens >= 0) AND (output_tokens >= 0) AND (candidate_context_tokens >= 0))),
    CONSTRAINT placement_assessments_tokenizer_nonempty CHECK ((btrim(tokenizer) <> ''::text))
);

ALTER TABLE ONLY public.placement_assessments FORCE ROW LEVEL SECURITY;


--
-- Name: placement_assessments placement_assessments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_pkey PRIMARY KEY (team_id, assessment_id);


--
-- Name: placement_assessments placement_assessments_team_id_claim_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_team_id_claim_key_key UNIQUE (team_id, claim_key);


--
-- Name: placement_assessments placement_assessments_team_id_placement_item_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_team_id_placement_item_id_key UNIQUE (team_id, placement_item_id);


--
-- Name: placement_assessments_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_assessments_owner_created_idx ON public.placement_assessments USING btree (team_id, owner_profile_id, created_at);


--
-- Name: placement_assessments_submission_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX placement_assessments_submission_owner_created_idx ON public.placement_assessments USING btree (team_id, owner_profile_id, created_at, placement_run_id) WHERE (assessment_scope = 'submission'::text);


--
-- Name: placement_assessments_submission_run_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX placement_assessments_submission_run_unique ON public.placement_assessments USING btree (team_id, placement_run_id) WHERE (assessment_scope = 'submission'::text);


--
-- Name: placement_assessments placement_assessments_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER placement_assessments_append_only BEFORE DELETE OR UPDATE ON public.placement_assessments FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: placement_assessments placement_assessments_claim_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_claim_ref FOREIGN KEY (team_id, placement_item_id, claim_key) REFERENCES public.placement_items(team_id, placement_item_id, claim_key) ON DELETE RESTRICT;


--
-- Name: placement_assessments placement_assessments_item_owner_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_item_owner_ref FOREIGN KEY (team_id, placement_item_id, owner_profile_id) REFERENCES public.placement_items(team_id, placement_item_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_assessments placement_assessments_submission_run_ref; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.placement_assessments
    ADD CONSTRAINT placement_assessments_submission_run_ref FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id) REFERENCES public.placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: placement_assessments; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.placement_assessments ENABLE ROW LEVEL SECURITY;

--
-- Name: placement_assessments placement_assessments_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_assessments_insert ON public.placement_assessments FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: placement_assessments placement_assessments_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY placement_assessments_select ON public.placement_assessments FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
