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
-- Name: embedding_reconciliation_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.embedding_reconciliation_runs (
    reconciliation_run_id uuid DEFAULT gen_random_uuid() NOT NULL,
    embedding_contract_id uuid NOT NULL,
    embedding_dimensions integer NOT NULL,
    local_run_date date NOT NULL,
    status text DEFAULT 'reserved'::text NOT NULL,
    candidate_cutoff timestamp with time zone DEFAULT now() NOT NULL,
    worker_id text DEFAULT ''::text NOT NULL,
    lease_token uuid,
    lease_until timestamp with time zone,
    canary_job_id uuid,
    canary_attempted_at timestamp with time zone,
    canary_outcome text DEFAULT ''::text NOT NULL,
    canary_failure_class text DEFAULT ''::text NOT NULL,
    canary_failure_code text DEFAULT ''::text NOT NULL,
    requeued_count bigint DEFAULT 0 NOT NULL,
    recovered_count bigint DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT embedding_reconciliation_runs_count_check CHECK (((requeued_count >= 0) AND (recovered_count >= 0))),
    CONSTRAINT embedding_reconciliation_runs_failure_contract_check CHECK ((((canary_failure_class = ''::text) AND (canary_failure_code = ''::text)) OR ((canary_failure_class = 'transient'::text) AND (canary_failure_code = ANY (ARRAY['provider_rate_limited'::text, 'provider_timeout'::text, 'provider_network_error'::text, 'provider_server_error'::text]))) OR ((canary_failure_class = 'provider_action_required'::text) AND (canary_failure_code = ANY (ARRAY['provider_quota_exhausted'::text, 'provider_authentication_failed'::text, 'provider_permission_denied'::text, 'provider_contract_rejected'::text, 'provider_response_invalid'::text]))) OR ((canary_failure_class = 'permanent'::text) AND (canary_failure_code = ANY (ARRAY['embedding_input_rejected'::text, 'embedding_contract_mismatch'::text, 'unknown_embedding_failure'::text]))))),
    CONSTRAINT embedding_reconciliation_runs_outcome_check CHECK ((canary_outcome = ANY (ARRAY[''::text, 'succeeded'::text, 'failed'::text, 'ambiguous'::text]))),
    CONSTRAINT embedding_reconciliation_runs_status_check CHECK ((status = ANY (ARRAY['reserved'::text, 'running'::text, 'completed'::text, 'deferred'::text, 'failed'::text, 'ambiguous'::text])))
);

ALTER TABLE ONLY public.embedding_reconciliation_runs FORCE ROW LEVEL SECURITY;


--
-- Name: embedding_reconciliation_runs embedding_reconciliation_runs_embedding_contract_id_embeddi_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_reconciliation_runs
    ADD CONSTRAINT embedding_reconciliation_runs_embedding_contract_id_embeddi_key UNIQUE (embedding_contract_id, embedding_dimensions, local_run_date);


--
-- Name: embedding_reconciliation_runs embedding_reconciliation_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_reconciliation_runs
    ADD CONSTRAINT embedding_reconciliation_runs_pkey PRIMARY KEY (reconciliation_run_id);


--
-- Name: embedding_reconciliation_runs embedding_reconciliation_runs_embedding_contract_id_embedd_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_reconciliation_runs
    ADD CONSTRAINT embedding_reconciliation_runs_embedding_contract_id_embedd_fkey FOREIGN KEY (embedding_contract_id, embedding_dimensions) REFERENCES public.embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT;


--
-- Name: embedding_reconciliation_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.embedding_reconciliation_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: embedding_reconciliation_runs embedding_reconciliation_runs_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY embedding_reconciliation_runs_system_access ON public.embedding_reconciliation_runs USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text]))) WITH CHECK ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- PostgreSQL database dump complete
--
