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
-- Name: evidence_security_signals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence_security_signals (
    team_id uuid NOT NULL,
    security_event_id uuid NOT NULL,
    signal_index integer NOT NULL,
    owner_profile_id uuid NOT NULL,
    kind text NOT NULL,
    severity text NOT NULL,
    span_start integer NOT NULL,
    span_end integer NOT NULL,
    quote text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT evidence_security_signal_index_check CHECK ((signal_index >= 0)),
    CONSTRAINT evidence_security_signal_kind_check CHECK ((kind = ANY (ARRAY['role_control_spoofing'::text, 'instruction_override'::text, 'prompt_secret_extraction'::text, 'tool_exfiltration'::text, 'obfuscated_instruction'::text, 'hidden_control_markup'::text]))),
    CONSTRAINT evidence_security_signal_metadata_object_check CHECK ((jsonb_typeof(metadata) = 'object'::text)),
    CONSTRAINT evidence_security_signal_severity_check CHECK ((severity = ANY (ARRAY['low'::text, 'medium'::text, 'high'::text, 'critical'::text]))),
    CONSTRAINT evidence_security_signal_span_check CHECK (((span_start >= 0) AND (span_end > span_start)))
);

ALTER TABLE ONLY public.evidence_security_signals FORCE ROW LEVEL SECURITY;


--
-- Name: evidence_security_signals evidence_security_signals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_signals
    ADD CONSTRAINT evidence_security_signals_pkey PRIMARY KEY (team_id, security_event_id, signal_index);


--
-- Name: evidence_security_signals evidence_security_signals_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER evidence_security_signals_append_only BEFORE DELETE OR UPDATE ON public.evidence_security_signals FOR EACH ROW EXECUTE FUNCTION public.prevent_append_only_mutation();


--
-- Name: evidence_security_signals evidence_security_signals_team_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_signals
    ADD CONSTRAINT evidence_security_signals_team_id_owner_profile_id_fkey FOREIGN KEY (team_id, owner_profile_id) REFERENCES public.semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;


--
-- Name: evidence_security_signals evidence_security_signals_team_id_security_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_signals
    ADD CONSTRAINT evidence_security_signals_team_id_security_event_id_fkey FOREIGN KEY (team_id, security_event_id) REFERENCES public.evidence_security_events(team_id, security_event_id) ON DELETE CASCADE;


--
-- Name: evidence_security_signals evidence_security_signals_team_id_security_event_id_owner__fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence_security_signals
    ADD CONSTRAINT evidence_security_signals_team_id_security_event_id_owner__fkey FOREIGN KEY (team_id, security_event_id, owner_profile_id) REFERENCES public.evidence_security_events(team_id, security_event_id, owner_profile_id) ON DELETE CASCADE;


--
-- Name: evidence_security_signals; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.evidence_security_signals ENABLE ROW LEVEL SECURITY;

--
-- Name: evidence_security_signals evidence_security_signals_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_security_signals_insert ON public.evidence_security_signals FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'profile'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid) AND (owner_profile_id = (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: evidence_security_signals evidence_security_signals_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY evidence_security_signals_select ON public.evidence_security_signals FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
