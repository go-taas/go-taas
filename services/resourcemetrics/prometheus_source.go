package resourcemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
)

// prometheusSource reads per-service utilization from Prometheus, which
// already scrapes cAdvisor (container CPU/memory) and the GPU exporter.
// Reading it instead of collecting and storing the same samples keeps one
// source of truth, writes nothing on the read path, and lets a range
// follow whatever Prometheus retains.
type prometheusSource struct {
	baseURL string
	client  *http.Client
	// namespace scopes the queries to the namespace inference pods live in.
	namespace string
	// gpuMetric is the PromQL metric carrying GPU utilization.
	gpuMetric string
}

// newPrometheusSource builds the reader. An empty baseURL means "not
// configured": the caller then falls back to the stored samples.
func newPrometheusSource(baseURL, namespace, gpuMetric string, timeout time.Duration) *prometheusSource {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if strings.TrimSpace(gpuMetric) == "" {
		gpuMetric = "DCGM_FI_DEV_GPU_UTIL"
	}
	return &prometheusSource{
		baseURL:   baseURL,
		client:    &http.Client{Timeout: timeout},
		namespace: namespace,
		gpuMetric: gpuMetric,
	}
}

// promSample is one point of a range query.
type promSample struct {
	timestamp int64
	value     float64
}

// promResponse is the subset of the Prometheus HTTP API this reader uses.
type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string   `json:"metric"`
			Value  []json.RawMessage   `json:"value"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// queryRangeByPod runs one range query and returns the points per pod, so
// the caller can build both the service-wide line (sum across pods) and
// the per-replica table from a single round trip.
func (p *prometheusSource) queryRangeByPod(ctx context.Context, query string, since, until int64, step time.Duration) (map[string][]promSample, error) {
	form := url.Values{}
	form.Set("query", query)
	form.Set("start", strconv.FormatInt(since, 10))
	form.Set("end", strconv.FormatInt(until, 10))
	form.Set("step", strconv.Itoa(maxInt(int(step.Seconds()), 1))+"s")

	body, err := p.do(ctx, "/api/v1/query_range", form)
	if err != nil {
		return nil, err
	}
	var resp promResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("resourcemetrics: decode prometheus response: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("resourcemetrics: prometheus query failed: %s", resp.Error)
	}

	out := make(map[string][]promSample, len(resp.Data.Result))
	for _, series := range resp.Data.Result {
		pod := series.Metric["pod"]
		points := make([]promSample, 0, len(series.Values))
		for _, pair := range series.Values {
			ts, val, err := decodePromPoint(pair)
			if err != nil {
				continue
			}
			points = append(points, promSample{timestamp: ts, value: val})
		}
		if len(points) == 0 {
			continue
		}
		sort.Slice(points, func(i, j int) bool { return points[i].timestamp < points[j].timestamp })
		out[pod] = points
	}
	return out, nil
}

// queryCount runs one instant query and reports whether the metric has
// any series at all. An idle GPU reports 0 — that is a value; a cluster
// without a GPU exporter reports nothing at all, and the two must look
// different on the page.
func (p *prometheusSource) queryCount(ctx context.Context, query string) (int, error) {
	form := url.Values{}
	form.Set("query", query)
	body, err := p.do(ctx, "/api/v1/query", form)
	if err != nil {
		return 0, err
	}
	var resp promResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("resourcemetrics: decode prometheus response: %w", err)
	}
	if resp.Status != "success" {
		return 0, fmt.Errorf("resourcemetrics: prometheus query failed: %s", resp.Error)
	}
	total := 0
	for _, series := range resp.Data.Result {
		if len(series.Value) != 2 {
			continue
		}
		_, val, err := decodePromPoint(series.Value)
		if err != nil {
			continue
		}
		total += int(val)
	}
	return total, nil
}

// do performs one API call and returns the body.
func (p *prometheusSource) do(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("resourcemetrics: build prometheus request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resourcemetrics: call prometheus: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("resourcemetrics: read prometheus response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resourcemetrics: prometheus returned %s", resp.Status)
	}
	return body, nil
}

// decodePromPoint decodes one [unixSeconds, "value"] pair.
func decodePromPoint(pair []json.RawMessage) (int64, float64, error) {
	if len(pair) != 2 {
		return 0, 0, fmt.Errorf("resourcemetrics: malformed prometheus point")
	}
	var ts float64
	if err := json.Unmarshal(pair[0], &ts); err != nil {
		return 0, 0, err
	}
	var raw string
	if err := json.Unmarshal(pair[1], &raw); err != nil {
		return 0, 0, err
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, 0, err
	}
	return int64(ts), val, nil
}

// podSelector renders a Prometheus label matcher for the exact pod names
// the caller resolved from the Kubernetes API. Exact names (rather than a
// name prefix) matter: a prefix also matches a deleted pod's replacement
// and another service whose name starts the same way, and the numbers
// would silently mix two services.
func podSelector(pods []string) string {
	parts := make([]string, 0, len(pods))
	for _, pod := range pods {
		pod = strings.TrimSpace(pod)
		if pod == "" {
			continue
		}
		parts = append(parts, regexpQuote(pod))
	}
	if len(parts) == 0 {
		return ""
	}
	return `pod=~"` + strings.Join(parts, "|") + `"`
}

// regexpQuote escapes a literal for a Prometheus (RE2) matcher.
func regexpQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// warnPrometheus logs a Prometheus failure and notes the fallback, so a
// misconfigured endpoint is visible without flooding the log per page view.
func warnPrometheus(err error) {
	logger.S().Warnw("resourcemetrics: prometheus read failed, using stored samples", "err", err)
}
