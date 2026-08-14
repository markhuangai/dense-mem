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
-- Name: relationship_correction_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.relationship_correction_events (
    team_id uuid NOT NULL,
    correction_id uuid DEFAULT gen_random_uuid() NOT NULL,
    submission_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    original_relationship_id uuid CONSTRAINT relationship_correction_event_original_relationship_id_not_null NOT NULL,
    original_relationship_version integer CONSTRAINT relationship_correction_eve_original_relationship_vers_not_null NOT NULL,
    successor_relationship_id uuid CONSTRAINT relationship_correction_even_successor_relationship_id_not_null NOT NULL,
    successor_relationship_version integer CONSTRAINT relationship_correction_eve_successor_relationship_ver_not_null NOT NULL,
    reused_successor boolean DEFAULT false NOT NULL,
    patch jsonb DEFAULT '{}'::jsonb NOT NULL,
    supports jsonb DEFAULT '[]'::jsonb NOT NULL,
    reason text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT relationship_correction_events_patch_check CHECK ((jsonb_typeof(patch) = 'object'::text)),
    CONSTRAINT relationship_correction_events_reason_check CHECK (((btrim(reason) <> ''::text) AND (char_length(reason) <= 1000))),
    CONSTRAINT relationship_correction_events_supports_check CHECK ((jsonb_typeof(supports) = 'array'::text)),
    CONSTRAINT relationship_correction_events_version_check CHECK (((original_relationship_version >= 1) AND (successor_relationship_version >= 1)))
);

ALTER TABLE ONLY public.relationship_correction_events FORCE ROW LEVEL SECURITY;


--
-- Name: relationship_correction_events relationship_correction_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_pkey PRIMARY KEY (team_id, correction_id);


--
-- Name: relationship_correction_events relationship_correction_events_submission_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_submission_unique UNIQUE (team_id, submission_id);


--
-- Name: relationship_correction_events_telemetry_window_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX relationship_correction_events_telemetry_window_idx ON public.relationship_correction_events USING btree (created_at, team_id, owner_profile_id);


--
-- Name: relationship_correction_events relationship_correction_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER relationship_correction_events_append_only BEFORE DELETE OR UPDATE ON public.relationship_correction_events FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: relationship_correction_events relationship_correction_events_original_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_original_fk FOREIGN KEY (team_id, original_relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_events relationship_correction_events_submission_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_submission_fk FOREIGN KEY (team_id, submission_id, owner_profile_id) REFERENCES public.relationship_correction_submissions(team_id, submission_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_events relationship_correction_events_successor_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.relationship_correction_events
    ADD CONSTRAINT relationship_correction_events_successor_fk FOREIGN KEY (team_id, successor_relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: relationship_correction_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.relationship_correction_events ENABLE ROW LEVEL SECURITY;

--
-- Name: relationship_correction_events relationship_correction_events_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_events_insert ON public.relationship_correction_events FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- Name: relationship_correction_events relationship_correction_events_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY relationship_correction_events_select ON public.relationship_correction_events FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
