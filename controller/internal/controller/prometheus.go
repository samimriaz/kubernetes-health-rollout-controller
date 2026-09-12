package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	rolloutv1alpha1 "github.com/samimriaz/kubernetes-health-rollout-controller/controller/api/v1alpha1"
)

var ErrNoData = errors.New("Prometheus query returned no data")

type SignalResult struct {
	RequestCount     float64
	ErrorRate        float64
	Latency          float64
	AdditionalChecks []MetricCheckResult
}

type MetricCheckResult struct {
	Name     string
	Value    float64
	MaxValue float64
}

type SignalProvider interface {
	Evaluate(context.Context, *rolloutv1alpha1.HealthGatedRolloutSpec) (SignalResult, error)
}

type PrometheusSignalProvider struct {
	Client *http.Client
}

func NewPrometheusSignalProvider() *PrometheusSignalProvider {
	return &PrometheusSignalProvider{Client: &http.Client{Timeout: 10 * time.Second}}
}

func (p *PrometheusSignalProvider) Evaluate(
	ctx context.Context,
	spec *rolloutv1alpha1.HealthGatedRolloutSpec,
) (SignalResult, error) {
	requestCount, err := p.query(ctx, spec.PrometheusURL, spec.RequestCountQuery)
	if err != nil {
		return SignalResult{}, fmt.Errorf("query request count: %w", err)
	}
	errorRate, err := p.query(ctx, spec.PrometheusURL, spec.ErrorRateQuery)
	if err != nil {
		return SignalResult{}, fmt.Errorf("query error rate: %w", err)
	}
	latency, err := p.query(ctx, spec.PrometheusURL, spec.LatencyQuery)
	if err != nil {
		return SignalResult{}, fmt.Errorf("query latency: %w", err)
	}
	additionalChecks := make([]MetricCheckResult, 0, len(spec.AdditionalChecks))
	for _, check := range spec.AdditionalChecks {
		value, err := p.query(ctx, spec.PrometheusURL, check.Query)
		if err != nil {
			return SignalResult{}, fmt.Errorf("query %s: %w", check.Name, err)
		}
		additionalChecks = append(additionalChecks, MetricCheckResult{
			Name: check.Name, Value: value, MaxValue: check.MaxValue.AsApproximateFloat64(),
		})
	}

	return SignalResult{
		RequestCount: requestCount, ErrorRate: errorRate, Latency: latency, AdditionalChecks: additionalChecks,
	}, nil
}

func (p *PrometheusSignalProvider) query(ctx context.Context, baseURL, query string) (float64, error) {
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/v1/query")
	if err != nil {
		return 0, fmt.Errorf("parse Prometheus URL: %w", err)
	}
	values := endpoint.Query()
	values.Set("query", query)
	endpoint.RawQuery = values.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("create Prometheus request: %w", err)
	}
	response, err := p.Client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("call Prometheus: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Prometheus returned HTTP %d", response.StatusCode)
	}

	var payload struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode Prometheus response: %w", err)
	}
	if payload.Status != "success" {
		return 0, fmt.Errorf("Prometheus query failed: %s", payload.Error)
	}
	if len(payload.Data.Result) == 0 || len(payload.Data.Result[0].Value) < 2 {
		return 0, ErrNoData
	}

	var value string
	if err := json.Unmarshal(payload.Data.Result[0].Value[1], &value); err != nil {
		return 0, fmt.Errorf("decode Prometheus value: %w", err)
	}
	result, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("parse Prometheus value %q: %w", value, err)
	}
	return result, nil
}
