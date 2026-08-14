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
-- Name: ownership_aliases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ownership_aliases (
    team_id uuid NOT NULL,
    legacy_owner_id uuid NOT NULL,
    canonical_identity_id uuid NOT NULL,
    credential_id uuid,
    reason text DEFAULT 'legacy_profile'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ownership_aliases_reason_check CHECK ((char_length(reason) <= 128))
);

ALTER TABLE ONLY public.ownership_aliases FORCE ROW LEVEL SECURITY;


--
-- Name: ownership_aliases ownership_aliases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ownership_aliases
    ADD CONSTRAINT ownership_aliases_pkey PRIMARY KEY (team_id, legacy_owner_id);


--
-- Name: idx_ownership_aliases_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ownership_aliases_canonical ON public.ownership_aliases USING btree (team_id, canonical_identity_id);


--
-- Name: ownership_aliases ownership_aliases_canonical_identity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ownership_aliases
    ADD CONSTRAINT ownership_aliases_canonical_identity_id_fkey FOREIGN KEY (canonical_identity_id) REFERENCES public.actor_identities(id) ON DELETE RESTRICT;


--
-- Name: ownership_aliases ownership_aliases_credential_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ownership_aliases
    ADD CONSTRAINT ownership_aliases_credential_id_fkey FOREIGN KEY (credential_id) REFERENCES public.credentials(id) ON DELETE SET NULL;


--
-- Name: ownership_aliases ownership_aliases_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ownership_aliases
    ADD CONSTRAINT ownership_aliases_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: ownership_aliases; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.ownership_aliases ENABLE ROW LEVEL SECURITY;

--
-- Name: ownership_aliases ownership_aliases_context_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY ownership_aliases_context_access ON public.ownership_aliases USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR (team_id = COALESCE((NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid, (NULLIF(current_setting('app.current_profile_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
