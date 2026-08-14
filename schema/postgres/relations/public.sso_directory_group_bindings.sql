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
-- Name: sso_directory_group_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sso_directory_group_bindings (
    connector_id uuid NOT NULL,
    group_id uuid NOT NULL,
    team_id uuid NOT NULL,
    origin text NOT NULL,
    scopes text[] DEFAULT ARRAY['read'::text] NOT NULL,
    role character varying(20) DEFAULT 'member'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT sso_directory_group_bindings_origin_check CHECK ((origin = ANY (ARRAY['directory_created'::text, 'exact_name'::text, 'adopted'::text]))),
    CONSTRAINT sso_directory_group_bindings_role_check CHECK (((role)::text = ANY ((ARRAY['manager'::character varying, 'member'::character varying])::text[])))
);

ALTER TABLE ONLY public.sso_directory_group_bindings FORCE ROW LEVEL SECURITY;


--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_bindings
    ADD CONSTRAINT sso_directory_group_bindings_pkey PRIMARY KEY (connector_id, group_id);


--
-- Name: idx_sso_directory_group_bindings_team; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sso_directory_group_bindings_team ON public.sso_directory_group_bindings USING btree (team_id);


--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_connector_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_bindings
    ADD CONSTRAINT sso_directory_group_bindings_connector_id_fkey FOREIGN KEY (connector_id) REFERENCES public.sso_directory_connectors(id) ON DELETE CASCADE;


--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_connector_id_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_bindings
    ADD CONSTRAINT sso_directory_group_bindings_connector_id_group_id_fkey FOREIGN KEY (connector_id, group_id) REFERENCES public.sso_directory_groups(connector_id, id) ON DELETE RESTRICT;


--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sso_directory_group_bindings
    ADD CONSTRAINT sso_directory_group_bindings_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;


--
-- Name: sso_directory_group_bindings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sso_directory_group_bindings ENABLE ROW LEVEL SECURITY;

--
-- Name: sso_directory_group_bindings sso_directory_group_bindings_system_access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY sso_directory_group_bindings_system_access ON public.sso_directory_group_bindings USING ((current_setting('app.tx_mode'::text, true) = 'system'::text)) WITH CHECK ((current_setting('app.tx_mode'::text, true) = 'system'::text));


--
-- PostgreSQL database dump complete
--
