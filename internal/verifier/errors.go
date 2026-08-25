package verifier

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

const providerFailureMaxRetryAfter = 5 * time.Minute

const (
	ProviderFailureClassTimeout             = modelprovider.ProviderFailureClassTimeout
	ProviderFailureClassRateLimited         = modelprovider.ProviderFailureClassRateLimited
	ProviderFailureClassHTTPClient          = modelprovider.ProviderFailureClassHTTPClient
	ProviderFailureClassHTTPServer          = modelprovider.ProviderFailureClassHTTPServer
	ProviderFailureClassHTTPUnexpected      = modelprovider.ProviderFailureClassHTTPUnexpected
	ProviderFailureClassTransport           = modelprovider.ProviderFailureClassTransport
	ProviderFailureClassProtocol            = modelprovider.ProviderFailureClassProtocol
	ProviderFailureClassRequestInvalid      = modelprovider.ProviderFailureClassRequestInvalid
	ProviderFailureClassProviderUnavailable = modelprovider.ProviderFailureClassProviderUnavailable
)

var (
	ErrVerifierTimeout           = modelprovider.ErrVerifierTimeout
	ErrVerifierProvider          = modelprovider.ErrVerifierProvider
	ErrVerifierRateLimit         = modelprovider.ErrVerifierRateLimit
	ErrVerifierMalformedResponse = modelprovider.ErrVerifierMalformedResponse
)

type TimeoutError = modelprovider.TimeoutError
type ProviderError = modelprovider.ProviderError
type RateLimitError = modelprovider.RateLimitError
type MalformedResponseError = modelprovider.MalformedResponseError
type FailureMeasurement = modelprovider.FailureMeasurement
type ProviderFailureMetadata = modelprovider.ProviderFailureMetadata

func ProviderFailureDetails(err error) ProviderFailureMetadata {
	return modelprovider.ProviderFailureDetails(err)
}
