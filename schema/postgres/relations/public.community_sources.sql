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
-- Name: community_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.community_sources (
    team_id uuid NOT NULL,
    community_id uuid NOT NULL,
    relationship_id uuid NOT NULL,
    owner_profile_id uuid NOT NULL,
    relationship_version integer NOT NULL,
    source_rank integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    semantic_group_key text DEFAULT ''::text NOT NULL,
    source_state_hash text DEFAULT ''::text NOT NULL,
    CONSTRAINT community_sources_rank_check CHECK ((source_rank >= 0)),
    CONSTRAINT community_sources_version_check CHECK ((relationship_version >= 1))
);

ALTER TABLE ONLY public.community_sources FORCE ROW LEVEL SECURITY;


--
-- Name: community_sources community_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_sources
    ADD CONSTRAINT community_sources_pkey PRIMARY KEY (team_id, community_id, relationship_id);


--
-- Name: community_sources_community_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_sources_community_idx ON public.community_sources USING btree (team_id, community_id);


--
-- Name: community_sources_group_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_sources_group_idx ON public.community_sources USING btree (team_id, semantic_group_key, community_id);


--
-- Name: community_sources_relationship_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX community_sources_relationship_idx ON public.community_sources USING btree (team_id, relationship_id, relationship_version);


--
-- Name: community_sources community_sources_team_id_community_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_sources
    ADD CONSTRAINT community_sources_team_id_community_id_fkey FOREIGN KEY (team_id, community_id) REFERENCES public.community_records(team_id, community_id) ON DELETE RESTRICT;


--
-- Name: community_sources community_sources_team_id_relationship_id_owner_profile_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.community_sources
    ADD CONSTRAINT community_sources_team_id_relationship_id_owner_profile_id_fkey FOREIGN KEY (team_id, relationship_id, owner_profile_id) REFERENCES public.relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT;


--
-- Name: community_sources; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.community_sources ENABLE ROW LEVEL SECURITY;

--
-- Name: community_sources community_sources_insert; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_sources_insert ON public.community_sources FOR INSERT WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_sources community_sources_select; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_sources_select ON public.community_sources FOR SELECT USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['team'::text, 'profile'::text])) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- Name: community_sources community_sources_update; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY community_sources_update ON public.community_sources FOR UPDATE USING (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid)))) WITH CHECK (((current_setting('app.tx_mode'::text, true) = ANY (ARRAY['system'::text, 'migration'::text])) OR ((current_setting('app.tx_mode'::text, true) = 'team'::text) AND (team_id = (NULLIF(current_setting('app.current_team_id'::text, true), ''::text))::uuid))));


--
-- PostgreSQL database dump complete
--
