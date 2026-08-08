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
-- Name: community_memberships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.community_memberships (
    team_id uuid NOT NULL,
    community_id uuid NOT NULL,
    entity_id uuid NOT NULL,
    rank integer NOT NULL,
    membership_score numeric(5,4) DEFAULT 1 NOT NULL,
    source_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT community_memberships_rank_check CHECK ((rank >= 0)),
    CONSTRAINT community_memberships_score_check CHECK (((membership_score >= (0)::numeric) AND (membership_score <= (1)::numeric))),
    CONSTRAINT community_memberships_source_count_check CHECK ((source_count >= 0))
);

ALTER TABLE ONLY public.community_memberships FORCE ROW LEVEL SECURITY;


--
-- Name: community_memberships community_memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_memberships
    ADD CONSTRAINT community_memberships_pkey PRIMARY KEY (team_id, community_id, entity_id);


--
-- Name: community_memberships_entity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_memberships_entity_idx ON public.community_memberships USING btree (team_id, entity_id, community_id);


--
-- Name: community_memberships community_memberships_team_id_community_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_memberships
    ADD CONSTRAINT community_memberships_team_id_community_id_fkey FOREIGN KEY (team_id, community_id) REFERENCES public.community_records(team_id, community_id) ON DELETE RESTRICT;


--
-- Name: community_memberships community_memberships_team_id_entity_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_memberships
    ADD CONSTRAINT community_memberships_team_id_entity_id_fkey FOREIGN KEY (team_id, entity_id) REFERENCES public.entity_records(team_id, entity_id) ON DELETE RESTRICT;


--
-- Name: community_memberships; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.community_memberships ENABLE ROW LEVEL SECURITY;

--
-- Name: community_memberships community_memberships_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_memberships_insert ON public.community_memberships FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_memberships community_memberships_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_memberships_select ON public.community_memberships FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_memberships community_memberships_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_memberships_update ON public.community_memberships FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
