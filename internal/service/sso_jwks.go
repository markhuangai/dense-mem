package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

const (
	oauthJWKSCacheTTL          = 5 * time.Minute
	oauthJWKSRefreshFloor      = time.Second
	oauthProviderResponseLimit = 1 << 20
)

type oauthJWKSProviderCache struct {
	mu                sync.Mutex
	set               jose.JSONWebKeySet
	uri               string
	sourceFingerprint string
	expiresAt         time.Time
	refreshedAt       time.Time
}

type oauthDiscoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

func (s *SSOService) oauthVerificationKey(ctx context.Context, provider domain.SSOProvider, keyID, algorithm string) (any, error) {
	cache := s.oauthProviderCache(provider.ID)
	set, fromCache, err := s.loadOAuthJWKS(ctx, provider, cache, false)
	if err != nil {
		return nil, err
	}
	keys := oauthCandidateKeys(set, keyID, algorithm)
	if len(keys) == 0 && fromCache {
		set, _, err = s.loadOAuthJWKS(ctx, provider, cache, true)
		if err != nil {
			return nil, err
		}
		keys = oauthCandidateKeys(set, keyID, algorithm)
	}
	if len(keys) != 1 {
		return nil, ErrOAuthTokenInvalid
	}
	return keys[0].Key, nil
}

func (s *SSOService) oauthProviderCache(providerID uuid.UUID) *oauthJWKSProviderCache {
	s.oauthCacheMu.Lock()
	defer s.oauthCacheMu.Unlock()
	cache := s.oauthCaches[providerID]
	if cache == nil {
		cache = &oauthJWKSProviderCache{}
		s.oauthCaches[providerID] = cache
	}
	return cache
}

func (s *SSOService) loadOAuthJWKS(ctx context.Context, provider domain.SSOProvider, cache *oauthJWKSProviderCache, force bool) (jose.JSONWebKeySet, bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := s.now().UTC()
	sourceFingerprint := oauthJWKSSourceFingerprint(provider)
	sourceMatches := cache.sourceFingerprint == sourceFingerprint
	if !force && sourceMatches && len(cache.set.Keys) > 0 && now.Before(cache.expiresAt) {
		return cache.set, true, nil
	}
	if force && sourceMatches && len(cache.set.Keys) > 0 && now.Sub(cache.refreshedAt) < oauthJWKSRefreshFloor {
		return cache.set, true, nil
	}

	runtime, err := s.runtimeConfig(ctx)
	if err != nil {
		return jose.JSONWebKeySet{}, false, fmt.Errorf("%w: runtime configuration", ErrOAuthProviderUnavailable)
	}
	providerCtx, cancel := s.providerContext(ctx, runtime)
	defer cancel()
	uri := provider.ProtectedResource.JWKSURI
	if provider.ProtectedResource.JWKSSource == "discovery" {
		uri, err = s.discoverOAuthJWKSURI(providerCtx, provider)
		if err != nil {
			return jose.JSONWebKeySet{}, false, err
		}
	}
	set, err := s.fetchOAuthJWKS(providerCtx, uri)
	if err != nil {
		return jose.JSONWebKeySet{}, false, err
	}
	cache.set = set
	cache.uri = uri
	cache.sourceFingerprint = sourceFingerprint
	cache.refreshedAt = now
	cache.expiresAt = now.Add(oauthJWKSCacheTTL)
	return cache.set, false, nil
}

func oauthJWKSSourceFingerprint(provider domain.SSOProvider) string {
	staticURI := ""
	if provider.ProtectedResource.JWKSSource == "static" {
		staticURI = provider.ProtectedResource.JWKSURI
	}
	return strings.Join([]string{provider.ProtectedResource.JWKSSource, provider.IssuerURL, staticURI}, "\x00")
}

func (s *SSOService) discoverOAuthJWKSURI(ctx context.Context, provider domain.SSOProvider) (string, error) {
	endpoint := strings.TrimRight(provider.IssuerURL, "/") + "/.well-known/openid-configuration"
	body, err := s.getOAuthProviderDocument(ctx, endpoint)
	if err != nil {
		return "", err
	}
	if err := jsonstrict.RejectDuplicateFields(body); err != nil {
		return "", ErrOAuthProviderUnavailable
	}
	var document oauthDiscoveryDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return "", ErrOAuthProviderUnavailable
	}
	if document.Issuer != provider.IssuerURL {
		return "", ErrOAuthProviderUnavailable
	}
	document.JWKSURI = strings.TrimSpace(document.JWKSURI)
	if err := validateProtectedResourceURL("discovery jwks_uri", document.JWKSURI); err != nil {
		return "", ErrOAuthProviderUnavailable
	}
	return document.JWKSURI, nil
}

func (s *SSOService) fetchOAuthJWKS(ctx context.Context, uri string) (jose.JSONWebKeySet, error) {
	body, err := s.getOAuthProviderDocument(ctx, uri)
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	if err := jsonstrict.RejectDuplicateFields(body); err != nil {
		return jose.JSONWebKeySet{}, ErrOAuthProviderUnavailable
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil || len(set.Keys) == 0 || len(set.Keys) > 100 {
		return jose.JSONWebKeySet{}, ErrOAuthProviderUnavailable
	}
	for _, key := range set.Keys {
		if !key.Valid() || !key.IsPublic() {
			return jose.JSONWebKeySet{}, ErrOAuthProviderUnavailable
		}
	}
	return set, nil
}

func (s *SSOService) getOAuthProviderDocument(ctx context.Context, uri string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, ErrOAuthProviderUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, ErrOAuthProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, ErrOAuthProviderUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, oauthProviderResponseLimit+1))
	if err != nil || len(body) == 0 || len(body) > oauthProviderResponseLimit {
		return nil, ErrOAuthProviderUnavailable
	}
	return body, nil
}

func oauthCandidateKeys(set jose.JSONWebKeySet, keyID, algorithm string) []jose.JSONWebKey {
	candidates := set.Keys
	if keyID != "" {
		candidates = set.Key(keyID)
	}
	result := make([]jose.JSONWebKey, 0, len(candidates))
	for _, key := range candidates {
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		if key.Algorithm != "" && key.Algorithm != algorithm {
			continue
		}
		result = append(result, key)
	}
	return result
}
