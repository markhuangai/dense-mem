package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIProvider_Embed_SendsBearerAuth(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "sk-123",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	vec, model, err := p.Embed(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, "Bearer sk-123", gotAuth)
	assert.Equal(t, "m", model)
	assert.Equal(t, []float32{0.1, 0.2}, vec)
	assert.NotContains(t, gotBody, "api_key", "API key must not appear in body")
}

func TestOpenAIProvider_EmbedBatch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Parse and verify request body
		var reqBody map[string]any
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		assert.Equal(t, "text-embedding-3-small", reqBody["model"])
		assert.Equal(t, float64(3), reqBody["dimensions"])

		input, ok := reqBody["input"].([]interface{})
		require.True(t, ok)
		assert.Len(t, input, 2)
		assert.Equal(t, "hello", input[0])
		assert.Equal(t, "world", input[1])

		// Return response with two embeddings
		resp := `{"data":[{"embedding":[0.1,0.2,0.3]},{"embedding":[0.4,0.5,0.6]}]}`
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "test-key",
		AIEmbeddingModel:          "text-embedding-3-small",
		AIEmbeddingDimensions:     3,
		AIEmbeddingTimeoutSeconds: 10,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	vecs, model, err := p.EmbedBatch(context.Background(), []string{"hello", "world"})
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-small", model)
	require.Len(t, vecs, 2)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, vecs[0])
	assert.Equal(t, []float32{0.4, 0.5, 0.6}, vecs[1])
}

func TestOpenAIProvider_Non200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "bad-key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.Embed(context.Background(), "test")
	require.Error(t, err)
	var httpErr *ProviderHTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, 401, httpErr.Status)
	assert.Contains(t, httpErr.Message, "Invalid API key")
}

func TestOpenAIProvider_NonJSONServerErrorIsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2.5")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream unavailable`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.Embed(context.Background(), "test")
	require.Error(t, err)
	var httpErr *ProviderHTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadGateway, httpErr.Status)
	assert.Contains(t, httpErr.Message, "non-JSON response")
	assert.Contains(t, httpErr.Message, "upstream unavailable")
	assert.Equal(t, 2500*time.Millisecond, httpErr.RetryAfter)
}

func TestOpenAIProvider_WrongDimensions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return embedding with wrong dimensions
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2, // Config says 2, but server returns 4
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.Embed(context.Background(), "test")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmbeddingProvider)
	assert.Contains(t, err.Error(), "expected 2 dimensions, got 4")
}

func TestOpenAIProvider_WrongNumberOfEmbeddings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return fewer embeddings than requested
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL,
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmbeddingProvider)
	assert.Contains(t, err.Error(), "expected 3 embeddings, got 1")
}

func TestOpenAIProvider_IsAvailable(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		key        string
		model      string
		dimensions int
		want       bool
	}{
		{
			name:       "all configured",
			url:        "https://api.example.com",
			key:        "key",
			model:      "model",
			dimensions: 1536,
			want:       true,
		},
		{
			name:       "missing URL",
			url:        "",
			key:        "key",
			model:      "model",
			dimensions: 1536,
			want:       false,
		},
		{
			name:       "missing key",
			url:        "https://api.example.com",
			key:        "",
			model:      "model",
			dimensions: 1536,
			want:       false,
		},
		{
			name:       "missing model",
			url:        "https://api.example.com",
			key:        "key",
			model:      "",
			dimensions: 1536,
			want:       false,
		},
		{
			name:       "zero dimensions",
			url:        "https://api.example.com",
			key:        "key",
			model:      "model",
			dimensions: 0,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AIAPIURL:                  tt.url,
				AIAPIKey:                  tt.key,
				AIEmbeddingModel:          tt.model,
				AIEmbeddingDimensions:     tt.dimensions,
				AIEmbeddingTimeoutSeconds: 5,
			}
			p := NewOpenAIEmbeddingProvider(cfg, nil)
			assert.Equal(t, tt.want, p.IsAvailable())
		})
	}
}

func TestOpenAIProvider_ModelNameAndDimensions(t *testing.T) {
	cfg := &config.Config{
		AIAPIURL:                  "https://api.example.com",
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "text-embedding-3-small",
		AIEmbeddingDimensions:     1536,
		AIEmbeddingTimeoutSeconds: 30,
	}
	p := NewOpenAIEmbeddingProvider(cfg, nil)

	assert.Equal(t, "text-embedding-3-small", p.ModelName())
	assert.Equal(t, 1536, p.Dimensions())
}

func TestOpenAIProvider_DefaultTimeout(t *testing.T) {
	cfg := &config.Config{
		AIAPIURL:                  "https://api.example.com",
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 0, // Zero timeout should default to 30s
	}
	p := NewOpenAIEmbeddingProvider(cfg, nil)

	assert.Equal(t, 30*time.Second, p.timeout)
}

func TestOpenAIProvider_NilHTTPClient(t *testing.T) {
	cfg := &config.Config{
		AIAPIURL:                  "https://api.example.com",
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     2,
		AIEmbeddingTimeoutSeconds: 10,
	}
	p := NewOpenAIEmbeddingProvider(cfg, nil)

	assert.NotNil(t, p.httpClient)
}

func TestOpenAIProvider_TrailingSlashInURL(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIAPIURL:                  srv.URL + "/", // Trailing slash
		AIAPIKey:                  "key",
		AIEmbeddingModel:          "m",
		AIEmbeddingDimensions:     1,
		AIEmbeddingTimeoutSeconds: 5,
	}
	p := NewOpenAIEmbeddingProvider(cfg, srv.Client())

	_, _, err := p.Embed(context.Background(), "test")
	require.NoError(t, err)
	assert.Equal(t, "/embeddings", gotURL)
}

func TestOpenAIProvider_ImplementsInterface(t *testing.T) {
	// Compile-time assertion that OpenAIEmbeddingProvider implements EmbeddingProviderInterface
	var _ EmbeddingProviderInterface = (*OpenAIEmbeddingProvider)(nil)
	assert.True(t, true, "compile-time interface assertion passed")
}
