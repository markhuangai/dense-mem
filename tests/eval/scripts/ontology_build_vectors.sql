CREATE TABLE ontology_study_topic_vectors (
    team_id uuid NOT NULL,
    shared_space_id uuid NOT NULL,
    space_generation bigint NOT NULL,
    record_id uuid NOT NULL,
    version bigint NOT NULL,
    document tsvector NOT NULL,
    PRIMARY KEY (team_id, shared_space_id, space_generation, record_id, version),
    FOREIGN KEY (team_id, shared_space_id, space_generation, record_id, version)
        REFERENCES ontology_topic_revision_payloads (team_id, shared_space_id, space_generation, record_id, version)
);

ALTER TABLE ontology_study_topic_vectors ENABLE ROW LEVEL SECURITY;
ALTER TABLE ontology_study_topic_vectors FORCE ROW LEVEL SECURITY;
DO $policy$
DECLARE read_expression text;
DECLARE write_expression text;
BEGIN
    SELECT qual INTO STRICT read_expression FROM pg_policies WHERE schemaname='public'
        AND tablename='ontology_topic_revision_payloads' AND policyname='feature_read';
    SELECT with_check INTO STRICT write_expression FROM pg_policies WHERE schemaname='public'
        AND tablename='ontology_topic_revision_payloads' AND policyname='feature_insert';
    EXECUTE format('CREATE POLICY study_read ON ontology_study_topic_vectors FOR SELECT USING (%s)',read_expression);
    EXECUTE format('CREATE POLICY study_insert ON ontology_study_topic_vectors FOR INSERT WITH CHECK (%s)',write_expression);
END;
$policy$;

CREATE FUNCTION ontology_study_make_vector(names text[], description text) RETURNS tsvector
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $document$
    SELECT __STUDY_VECTOR_EXPRESSION__
$document$;

-- The study renders the indexed vector and both query binds from the same frozen stop-word list.
CREATE FUNCTION ontology_study_cached(document tsvector, bound_stopwords text[]) RETURNS tsvector
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $read$ SELECT document $read$;

CREATE FUNCTION ontology_study_publish_vector() RETURNS trigger
LANGUAGE plpgsql AS $publication$
DECLARE description text;
BEGIN
    SELECT COALESCE(body->'definition'->>'description','') INTO STRICT description
        FROM ontology_topic_revision_payloads
        WHERE (team_id,shared_space_id,space_generation,record_id,version)=
              (NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id,NEW.version);
    INSERT INTO ontology_study_topic_vectors(team_id,shared_space_id,space_generation,record_id,version,document)
    VALUES(NEW.team_id,NEW.shared_space_id,NEW.space_generation,NEW.record_id,NEW.version,
        ontology_study_make_vector(NEW.names,description));
    RETURN NULL;
END;
$publication$;

CREATE TRIGGER ontology_study_publish_vector AFTER INSERT OR UPDATE OF version ON ontology_record_heads
FOR EACH ROW WHEN (NEW.kind='topic' AND NOT NEW.retired) EXECUTE FUNCTION ontology_study_publish_vector();

-- study phase: backfill
INSERT INTO ontology_study_topic_vectors(team_id,shared_space_id,space_generation,record_id,version,document)
    SELECT head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version,
           ontology_study_make_vector(head.names,COALESCE(payload.body->'definition'->>'description',''))
    FROM ontology_record_heads head JOIN ontology_topic_revision_payloads payload
        USING(team_id,shared_space_id,space_generation,record_id,version)
    WHERE head.kind='topic' AND NOT head.retired;

-- study phase: index
CREATE INDEX ontology_study_topic_vectors_gin ON ontology_study_topic_vectors USING gin(document);
ANALYZE ontology_study_topic_vectors;
