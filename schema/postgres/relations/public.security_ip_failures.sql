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
-- Name: security_ip_failures; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.security_ip_failures (
    ip text NOT NULL,
    failure_count integer DEFAULT 0 NOT NULL,
    first_failed_at timestamp with time zone NOT NULL,
    last_failed_at timestamp with time zone NOT NULL,
    last_reason text DEFAULT ''::text NOT NULL,
    last_surface text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT security_ip_failures_failure_count_check CHECK ((failure_count >= 0))
);


--
-- Name: security_ip_failures security_ip_failures_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_ip_failures
    ADD CONSTRAINT security_ip_failures_pkey PRIMARY KEY (ip);


--
-- PostgreSQL database dump complete
--
