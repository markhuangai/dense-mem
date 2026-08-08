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
-- Name: embedding_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.embedding_config (
    id smallint DEFAULT 1 NOT NULL,
    model character varying(255) NOT NULL,
    dimensions integer NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT embedding_config_singleton CHECK ((id = 1))
);


--
-- Name: embedding_config embedding_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.embedding_config
    ADD CONSTRAINT embedding_config_pkey PRIMARY KEY (id);


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
