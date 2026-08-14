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
-- Name: security_ip_bans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.security_ip_bans (
    ip text NOT NULL,
    reason text NOT NULL,
    source character varying(16) NOT NULL,
    failure_count integer DEFAULT 0 NOT NULL,
    banned_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone,
    last_failed_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT security_ip_bans_failure_count_check CHECK ((failure_count >= 0)),
    CONSTRAINT security_ip_bans_source_check CHECK (((source)::text = ANY ((ARRAY['auto'::character varying, 'manual'::character varying])::text[])))
);


--
-- Name: security_ip_bans security_ip_bans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_ip_bans
    ADD CONSTRAINT security_ip_bans_pkey PRIMARY KEY (ip);


--
-- Name: idx_security_ip_bans_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_ip_bans_active ON public.security_ip_bans USING btree (ip) WHERE (revoked_at IS NULL);


--
-- Name: idx_security_ip_bans_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_ip_bans_expires_at ON public.security_ip_bans USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- PostgreSQL database dump complete
--
