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
-- Name: relationship_correction_submissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_correction_submissions (
    team_id uuid NOT NULL,
    submission_id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_profile_id uuid NOT NULL,
    relationship_id uuid NOT NULL,
    expected_version integer NOT NULL,
    request_hash text NOT NULL,
    patch jsonb DEFAULT '{}'::jsonb NOT NULL,
    supports jsonb DEFAULT '[]'::jsonb NOT NULL,
    reason text NOT NULL,
    idempotency_key text NOT NULL,
    confirmation_idempotency_key text DEFAULT ''::text CONSTRAINT relationship_correction_sub_confirmation_idempotency_k_not_null NOT NULL,
    confirmation_request_hash text DEFAULT ''::text CONSTRAINT relationship_correction_subm_confirmation_request_hash_not_null NOT NULL,
    processing_state text NOT NULL,
    confirmation_round integer DEFAULT 0 NOT NULL,
    confirmation_token text DEFAULT ''::text NOT NULL,
    confirmation_expires_at timestamp with time zone,
    candidates jsonb DEFAULT '[]'::jsonb NOT NULL,
    selection jsonb DEFAULT '{}'::jsonb NOT NULL,
    successor_relationship_id uuid,
    reused_successor boolean DEFAULT false NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT relationship_correction_submissions_candidates_check CHECK ((jsonb_typeof(candidates) = 'array'::text)),
    CONSTRAINT relationship_correction_submissions_confirmation_idempotency_ch CHECK ((((confirmation_idempotency_key = ''::text) AND (confirmation_request_hash = ''::text)) OR ((btrim(confirmation_idempotency_key) <> ''::text) AND (btrim(confirmation_request_hash) <> ''::text)))),
    CONSTRAINT relationship_correction_submissions_confirmation_round_check CHECK (((confirmation_round >= 0) AND (confirmation_round <= 1))),
    CONSTRAINT relationship_correction_submissions_confirmation_state_check CHECK ((((processing_state = 'awaiting_confirmation'::text) AND (confirmation_round = 0) AND (btrim(confirmation_token) <> ''::text) AND (confirmation_expires_at IS NOT NULL) AND (jsonb_array_length(candidates) > 1)) OR (processing_state <> 'awaiting_confirmation'::text))),
    CONSTRAINT relationship_correction_submissions_expected_version_check CHECK ((expected_version >= 1)),
    CONSTRAINT relationship_correction_submissions_idempotency_check CHECK ((btrim(idempotency_key) <> ''::text)),
    CONSTRAINT relationship_correction_submissions_patch_check CHECK ((jsonb_typeof(patch) = 'object'::text)),
    CONSTRAINT relationship_correction_submissions_reason_check CHECK (((btrim(reason) <> ''::text) AND (char_length(reason) <= 1000))),
    CONSTRAINT relationship_correction_submissions_request_hash_check CHECK ((btrim(request_hash) <> ''::text)),
    CONSTRAINT relationship_correction_submissions_result_check CHECK ((((processing_state = 'completed'::text) AND (successor_relationship_id IS NOT NULL)) OR (processing_state <> 'completed'::text))),
    CONSTRAINT relationship_correction_submissions_selection_check CHECK ((jsonb_typeof(selection) = 'object'::text)),
    CONSTRAINT relationship_correction_submissions_state_check CHECK ((processing_state = ANY (ARRAY['processing'::text, 'awaiting_confirmation'::text, 'completed'::text, 'rejected'::text, 'failed'::text]))),
    CONSTRAINT relationship_correction_submissions_supports_check CHECK ((jsonb_typeof(supports) = 'array'::text)),
    CONSTRAINT relationship_correction_submissions_terminal_state_check CHECK ((((processing_state = ANY (ARRAY['completed'::text, 'rejected'::text, 'failed'::text])) AND (completed_at IS NOT NULL)) OR (processing_state = ANY (ARRAY['processing'::text, 'awaiting_confirmation'::text]))))
);

ALTER TABLE ONLY public.relationship_correction_submissions FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_correction_submissions relationship_correction_submissions_owner_ref_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_owner_ref_unique UNIQUE (team_id, submission_id, owner_profile_id);


--
-- Name: relationship_correction_submissions relationship_correction_submissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_pkey PRIMARY KEY (team_id, submission_id);


--
-- Name: relationship_correction_submissions_confirmation_idempotency_un; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_correction_submissions_confirmation_idempotency_un ON public.relationship_correction_submissions USING btree (team_id, owner_profile_id, confirmation_idempotency_key) WHERE (confirmation_idempotency_key <> ''::text);


--
-- Name: relationship_correction_submissions_idempotency_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX relationship_correction_submissions_idempotency_unique ON public.relationship_correction_submissions USING btree (team_id, owner_profile_id, idempotency_key);


--
-- Name: relationship_correction_submissions_owner_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_correction_submissions_owner_created_idx ON public.relationship_correction_submissions USING btree (team_id, owner_profile_id, created_at DESC, submission_id DESC);


--
-- Name: relationship_correction_submissions relationship_correction_submissions_owner_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_owner_fk FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_submissions relationship_correction_submissions_successor_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_successor_fk FOREIGN KEY (team_id, successor_relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_submissions relationship_correction_submissions_target_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_submissions
    ADD CONSTRAINT relationship_correction_submissions_target_fk FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_submissions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_correction_submissions ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_correction_submissions relationship_correction_submissions_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_submissions_insert ON public.relationship_correction_submissions FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_correction_submissions relationship_correction_submissions_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_submissions_select ON public.relationship_correction_submissions FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_correction_submissions relationship_correction_submissions_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_submissions_update ON public.relationship_correction_submissions FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
