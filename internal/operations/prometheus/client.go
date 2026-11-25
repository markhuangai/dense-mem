package prometheus

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

type Client struct {
	query     v1.API
	configErr error
}

func NewClient(baseURL string, timeout time.Duration) operationscontract.TelemetryQuerier {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	transport, err := api.NewClient(api.Config{Address: baseURL,
		Client: &http.Client{Timeout: operationscontract.NormalizeTelemetryQueryTimeout(timeout)}})
	if err != nil {
		return &Client{configErr: queryError("request_url_invalid", err)}
	}
	return &Client{query: v1.NewAPI(transport)}
}

func queryError(reason string, cause error) error {
	return &operationscontract.TelemetryQueryError{Reason: reason, Cause: cause}
}

func prometheusQueryError(err error) error {
	var failure *v1.Error
	if errors.As(err, &failure) {
		switch failure.Type {
		case v1.ErrBadResponse:
			return queryError("response_decode_failed", err)
		case v1.ErrServer, v1.ErrClient:
			return queryError("http_status", err)
		default:
			return queryError("prometheus_api_error", err)
		}
	}
	return queryError(operationscontract.TelemetryQueryFailureReason(err), err)
}

func (s *Client) Range(ctx context.Context, query string, from, to time.Time, step time.Duration) ([]operationscontract.TelemetryPoint, error) {
	if s.configErr != nil {
		return nil, s.configErr
	}
	value, _, _, err := s.query.QueryRange(ctx, query, v1.Range{Start: from.Truncate(time.Second), End: to.Truncate(time.Second), Step: step})
	if err != nil {
		return nil, prometheusQueryError(err)
	}
	matrix, ok := value.(model.Matrix)
	if !ok {
		return nil, queryError("response_decode_failed", nil)
	}
	points := []operationscontract.TelemetryPoint{}
	if len(matrix) == 0 {
		return points, nil
	}
	for _, sample := range matrix[0].Values {
		scalar := finiteSample(sample.Value)
		if scalar.Available {
			points = append(points, operationscontract.TelemetryPoint{
				Timestamp: sample.Timestamp.Time().UTC().Format(time.RFC3339), Value: scalar.Value,
			})
		}
	}
	return points, nil
}

func (s *Client) Instant(ctx context.Context, query string) (operationscontract.TelemetryScalar, error) {
	if s.configErr != nil {
		return operationscontract.TelemetryScalar{}, s.configErr
	}
	value, _, _, err := s.query.Query(ctx, query, time.Time{})
	if err != nil {
		return operationscontract.TelemetryScalar{}, prometheusQueryError(err)
	}
	switch result := value.(type) {
	case model.Vector:
		if len(result) == 0 {
			return operationscontract.TelemetryScalar{}, nil
		}
		scalar := finiteSample(result[0].Value)
		scalar.Labels = make(map[string]string, len(result[0].Metric))
		for key, value := range result[0].Metric {
			scalar.Labels[string(key)] = string(value)
		}
		return scalar, nil
	case *model.Scalar:
		return finiteSample(result.Value), nil
	default:
		return operationscontract.TelemetryScalar{}, queryError("response_decode_failed", nil)
	}
}

func finiteSample(sample model.SampleValue) operationscontract.TelemetryScalar {
	value := float64(sample)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return operationscontract.TelemetryScalar{}
	}
	return operationscontract.TelemetryScalar{Value: max(value, 0), Available: true}
}
