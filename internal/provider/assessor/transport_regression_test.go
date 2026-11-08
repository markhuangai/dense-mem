package assessorprovider

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOpenAIRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, 17, openAIRetryAfterSeconds("17", now))
	assert.Equal(t, 30, openAIRetryAfterSeconds(now.Add(30*time.Second).Format(http.TimeFormat), now))
	assert.Equal(t, 300, openAIRetryAfterSeconds("999999999999", now))
	assert.Equal(t, 300, openAIRetryAfterSeconds(now.Add(time.Hour).Format(http.TimeFormat), now))
	assert.Zero(t, openAIRetryAfterSeconds("-1", now))
	assert.Zero(t, openAIRetryAfterSeconds("invalid", now))
}
