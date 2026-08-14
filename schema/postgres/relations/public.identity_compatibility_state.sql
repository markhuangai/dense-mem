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
-- Name: identity_compatibility_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.identity_compatibility_state (
    singleton boolean DEFAULT true NOT NULL,
    bridge_version text NOT NULL,
    state text NOT NULL,
    legacy_profile_count bigint DEFAULT 0 NOT NULL,
    identity_count bigint DEFAULT 0 NOT NULL,
    membership_count bigint DEFAULT 0 NOT NULL,
    credential_count bigint DEFAULT 0 NOT NULL,
    alias_count bigint DEFAULT 0 NOT NULL,
    unresolved_count bigint DEFAULT 0 NOT NULL,
    backup_checkpoint text DEFAULT ''::text NOT NULL,
    deployment_fingerprint text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT identity_compatibility_state_alias_count_check CHECK ((alias_count >= 0)),
    CONSTRAINT identity_compatibility_state_credential_count_check CHECK ((credential_count >= 0)),
    CONSTRAINT identity_compatibility_state_identity_count_check CHECK ((identity_count >= 0)),
    CONSTRAINT identity_compatibility_state_legacy_profile_count_check CHECK ((legacy_profile_count >= 0)),
    CONSTRAINT identity_compatibility_state_membership_count_check CHECK ((membership_count >= 0)),
    CONSTRAINT identity_compatibility_state_singleton_check CHECK (singleton),
    CONSTRAINT identity_compatibility_state_state_check CHECK ((state = ANY (ARRAY['bridge_active'::text, 'reconciled'::text, 'cutover_ready'::text, 'cleanup_complete'::text]))),
    CONSTRAINT identity_compatibility_state_unresolved_count_check CHECK ((unresolved_count >= 0))
);

ALTER TABLE ONLY public.identity_compatibility_state FORCE ROW LEVEL SECURITY;


--
-- Name: identity_compatibility_state identity_compatibility_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.identity_compatibility_state
    ADD CONSTRAINT identity_compatibility_state_pkey PRIMARY KEY (singleton);


--
-- Name: identity_compatibility_state; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.identity_compatibility_state ENABLE ROW LEVEL SECURITY;

--
-- Name: identity_compatibility_state identity_compatibility_state_context_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY identity_compatibility_state_context_access ON public.identity_compatibility_state USING ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text]))) WITH CHECK ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])));


--
-- PostgreSQL database dump complete
--
