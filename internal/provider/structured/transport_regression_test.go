package structured

import (
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOpenAIRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, 17, modelprovider.RetryAfterSeconds("17", now))
	assert.Equal(t, 30, modelprovider.RetryAfterSeconds(now.Add(30*time.Second).Format(http.TimeFormat), now))
	assert.Equal(t, 300, modelprovider.RetryAfterSeconds("999999999999", now))
	assert.Equal(t, 300, modelprovider.RetryAfterSeconds(now.Add(time.Hour).Format(http.TimeFormat), now))
	assert.Zero(t, modelprovider.RetryAfterSeconds("-1", now))
	assert.Zero(t, modelprovider.RetryAfterSeconds("invalid", now))
}
