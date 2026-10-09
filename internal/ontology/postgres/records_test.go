package postgres

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestStoredOntologyRecordDecodeRetainsClosedValidation(t *testing.T) {
	record := testTopic("database")
	record.Version = 1
	record.Fingerprint = testHash("database")
	body, err := json.Marshal(record)
	require.NoError(t, err)
	decoded, err := decodeRecord(body)
	require.NoError(t, err)
	require.Equal(t, record, decoded)
	for name, input := range map[string]string{
		"unknown field":          strings.TrimSuffix(string(body), "}") + `,"extra":true}`,
		"unknown nested field":   strings.Replace(string(body), `"key":"database"`, `"key":"database","extra":true`, 1),
		"oversized body":         strings.Repeat(" ", 65537),
		"trailing value":         string(body) + `{}`,
		"distant trailing value": string(body) + strings.Repeat(" ", 700) + `{}`,
		"non-JSON whitespace":    string(body) + "\u00a0",
		"malformed JSON":         string(body[:len(body)-1]),
		"invalid domain kind":    strings.Replace(string(body), `"topic"`, `"unsupported"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeRecord([]byte(input))
			require.Error(t, err)
			decoder := newStoredRecordDecoder()
			_, err = decoder.decode(body)
			require.NoError(t, err)
			_, err = decoder.decode([]byte(input))
			require.Error(t, err)
		})
	}
}

func TestStoredOntologyRecordDecoderKeepsRowsIndependent(t *testing.T) {
	group := ontology.Record{ID: uuid.NewString(), Version: 1, Kind: ontology.EvidenceGroup, Fingerprint: testHash("group"), Group: &ontology.Group{}}
	for range 60 {
		source := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: uuid.NewString(), Version: 1}
		group.Group.Members = append(group.Group.Members, source)
		group.Sources = append(group.Sources, ontology.SourceDependency{SourceHandle: source, Fingerprint: testHash(source.ID)})
	}
	topic := testTopic("database")
	topic.Version, topic.Fingerprint = 1, testHash("database")
	decoder := newStoredRecordDecoder()
	decoded := []ontology.Record{}
	for _, record := range []ontology.Record{group, topic, group} {
		body, err := json.Marshal(record)
		require.NoError(t, err)
		body = append(body, []byte(strings.Repeat(" \t\r\n", 200))...)
		value, err := decoder.decode(body)
		require.NoError(t, err)
		require.Equal(t, record, value)
		decoded = append(decoded, value)
	}
	require.Equal(t, group, decoded[0])
	require.Nil(t, decoded[1].Group)
	require.Nil(t, decoded[2].Definition)
}
