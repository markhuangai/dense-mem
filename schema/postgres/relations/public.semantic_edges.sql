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

--
-- Name: semantic_edges; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.semantic_edges WITH (security_invoker='true') AS
 SELECT relationship_id,
    team_id,
    owner_profile_id,
    semantic_group_key,
    subject_entity_id,
    predicate_key,
    predicate_version,
    object_entity_id,
    object_value_id,
    relationship_kind,
    current_cardinality,
    polarity,
    scope_key,
    valid_from,
    valid_to,
    support_count,
    source_group_count,
    version
   FROM public.relationship_records
  WHERE ((identity_alias_of_relationship_id IS NULL) AND (status = 'active'::text) AND (support_count > 0));


--
-- PostgreSQL database dump complete
--

\unrestrict DenseMemSchemaCatalogV1
