package postgres

import (
	"encoding/json"
	"strings"
	"testing"

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
		"unknown field":        strings.TrimSuffix(string(body), "}") + `,"extra":true}`,
		"unknown nested field": strings.Replace(string(body), `"key":"database"`, `"key":"database","extra":true`, 1),
		"oversized body":       strings.Repeat(" ", 65537),
		"trailing value":       string(body) + `{}`,
		"malformed JSON":       string(body[:len(body)-1]),
		"invalid domain kind":  strings.Replace(string(body), `"topic"`, `"unsupported"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeRecord([]byte(input))
			require.Error(t, err)
		})
	}
}
