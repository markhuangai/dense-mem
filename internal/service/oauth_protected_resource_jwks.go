package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

const (
	oauthJWKSCacheTTL          = 5 * time.Minute
	oauthJWKSRefreshFloor      = time.Second
	oauthProviderDocumentLimit = 1 << 20
)

type oauthJWKSCache struct {
	mu                     sync.Mutex
	set                    jose.JSONWebKeySet
	sourceFingerprint      string
	expiresAt              time.Time
	lastAttemptAt          time.Time
	lastAttemptFingerprint string
	lastAttemptForced      bool
	lastAttemptError       error
}

type oauthDiscoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

func (validator *OAuthProtectedResourceValidator) verificationKey(ctx context.Context, profile domain.OAuthProtectedResourceProfile, keyID, algorithm string) (any, error) {
	cache := validator.cacheForProfile(profile.Name)
	set, fromCache, err := validator.loadOAuthJWKS(ctx, profile, cache, false)
	if err != nil {
		return nil, err
	}
	keys := oauthCandidateKeys(set, keyID, algorithm)
	if len(keys) == 0 && fromCache {
		set, _, err = validator.loadOAuthJWKS(ctx, profile, cache, true)
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

func (validator *OAuthProtectedResourceValidator) cacheForProfile(name string) *oauthJWKSCache {
	validator.cachesMu.Lock()
	defer validator.cachesMu.Unlock()
	cache := validator.caches[name]
	if cache == nil {
		cache = &oauthJWKSCache{}
		validator.caches[name] = cache
	}
	return cache
}

func (validator *OAuthProtectedResourceValidator) loadOAuthJWKS(ctx context.Context, profile domain.OAuthProtectedResourceProfile, cache *oauthJWKSCache, force bool) (jose.JSONWebKeySet, bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	now := validator.now().UTC()
	fingerprint := oauthJWKSSourceFingerprint(profile)
	sourceMatches := cache.sourceFingerprint == fingerprint
	if !force && sourceMatches && len(cache.set.Keys) > 0 && now.Before(cache.expiresAt) {
		return cache.set, true, nil
	}
	if cache.lastAttemptFingerprint == fingerprint &&
		!cache.lastAttemptAt.IsZero() &&
		now.Sub(cache.lastAttemptAt) < oauthJWKSRefreshFloor &&
		(!force || cache.lastAttemptForced) {
		if cache.lastAttemptError != nil {
			return jose.JSONWebKeySet{}, sourceMatches, cache.lastAttemptError
		}
		return cache.set, sourceMatches, nil
	}

	cache.lastAttemptAt = now
	cache.lastAttemptFingerprint = fingerprint
	cache.lastAttemptForced = force
	cache.lastAttemptError = nil
	providerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthProviderTimeout)
	defer cancel()

	uri := profile.ProtectedResource.JWKSURI
	var err error
	if profile.ProtectedResource.JWKSSource == "discovery" {
		uri, err = validator.discoverOAuthJWKSURI(providerCtx, profile)
		if err != nil {
			cache.lastAttemptError = err
			return jose.JSONWebKeySet{}, sourceMatches, err
		}
	}
	set, err := validator.fetchOAuthJWKS(providerCtx, uri)
	if err != nil {
		cache.lastAttemptError = err
		return jose.JSONWebKeySet{}, sourceMatches, err
	}
	cache.set = set
	cache.sourceFingerprint = fingerprint
	cache.expiresAt = now.Add(oauthJWKSCacheTTL)
	return cache.set, false, nil
}

func oauthJWKSSourceFingerprint(profile domain.OAuthProtectedResourceProfile) string {
	return strings.Join([]string{
		profile.Issuer,
		profile.ProtectedResource.JWKSSource,
		profile.ProtectedResource.JWKSURI,
	}, "\x00")
}

func (validator *OAuthProtectedResourceValidator) discoverOAuthJWKSURI(ctx context.Context, profile domain.OAuthProtectedResourceProfile) (string, error) {
	endpoint := strings.TrimSuffix(profile.Issuer, "/") + "/.well-known/openid-configuration"
	body, err := validator.getOAuthProviderDocument(ctx, endpoint)
	if err != nil {
		return "", err
	}
	if err := jsonstrict.RejectDuplicateFields(body); err != nil {
		return "", ErrOAuthProviderUnavailable
	}
	var document oauthDiscoveryDocument
	if err := json.Unmarshal(body, &document); err != nil || document.Issuer != profile.Issuer {
		return "", ErrOAuthProviderUnavailable
	}
	if err := validateOAuthHTTPSURL(document.JWKSURI, true); err != nil {
		return "", ErrOAuthProviderUnavailable
	}
	return document.JWKSURI, nil
}

func (validator *OAuthProtectedResourceValidator) fetchOAuthJWKS(ctx context.Context, uri string) (jose.JSONWebKeySet, error) {
	body, err := validator.getOAuthProviderDocument(ctx, uri)
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

func (validator *OAuthProtectedResourceValidator) getOAuthProviderDocument(ctx context.Context, uri string) ([]byte, error) {
	if err := validateOAuthHTTPSURL(uri, true); err != nil {
		return nil, ErrOAuthProviderUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, ErrOAuthProviderUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := validator.httpClient.Do(request)
	if err != nil {
		return nil, ErrOAuthProviderUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.Request == nil || response.Request.URL == nil || validateOAuthHTTPSURL(response.Request.URL.String(), true) != nil {
		return nil, ErrOAuthProviderUnavailable
	}
	if response.StatusCode < http.StatusOK || response.StatusCode > http.StatusMultipleChoices-1 {
		return nil, ErrOAuthProviderUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, oauthProviderDocumentLimit+1))
	if err != nil || len(body) == 0 || len(body) > oauthProviderDocumentLimit {
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
