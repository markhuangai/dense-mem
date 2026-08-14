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
-- Name: v2_migration_operator_actions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.v2_migration_operator_actions (
    action_id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid,
    action text NOT NULL,
    actor text NOT NULL,
    remote_ip text DEFAULT ''::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT v2_migration_operator_actions_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);

ALTER TABLE ONLY public.v2_migration_operator_actions FORCE ROW LEVEL SECURITY;


--
-- Name: v2_migration_operator_actions v2_migration_operator_actions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_operator_actions
    ADD CONSTRAINT v2_migration_operator_actions_pkey PRIMARY KEY (action_id);


--
-- Name: idx_v2_migration_operator_actions_run_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_v2_migration_operator_actions_run_created ON public.v2_migration_operator_actions USING btree (run_id, created_at DESC);


--
-- Name: v2_migration_operator_actions v2_migration_operator_actions_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.v2_migration_operator_actions
    ADD CONSTRAINT v2_migration_operator_actions_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.v2_migration_runs(run_id) ON DELETE SET NULL;


--
-- Name: v2_migration_operator_actions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.v2_migration_operator_actions ENABLE ROW LEVEL SECURITY;

--
-- Name: v2_migration_operator_actions v2_migration_operator_actions_system_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY v2_migration_operator_actions_system_select ON public.v2_migration_operator_actions FOR SELECT USING ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
