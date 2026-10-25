package resourcemetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePodLister returns a fixed pod set.
type fakePodLister struct {
	pods []ServicePod
	err  error
}

func (f fakePodLister) ServicePods(context.Context, string) ([]ServicePod, error) {
	return f.pods, f.err
}

// promServer is a fake Prometheus: it answers each query with the values
// registered for the metric name it recognises.
type promServer struct {
	*httptest.Server
	// rangeValues is keyed by a substring of the query ("cpu", "memory",
	// "gpu"); its value is repeated at each timestamp.
	rangeValues map[string]string
	instant     map[string]string
}

func newPromServer(t *testing.T, rangeValues map[string]string, instant map[string]string) *promServer {
	t.Helper()
	p := &promServer{rangeValues: rangeValues, instant: instant}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		query := r.Form.Get("query")
		result := make([]any, 0)
		if r.URL.Path == "/api/v1/query_range" {
			value := ""
			for key, v := range p.rangeValues {
				if contains(query, key) {
					value = v
					break
				}
			}
			points := make([]any, 0, 3)
			if value != "" {
				for i := 0; i < 3; i++ {
					points = append(points, []any{float64(1700000000 + i*60), value})
				}
			}
			if len(points) > 0 {
				result = append(result, map[string]any{"metric": map[string]string{"pod": "svc-abc"}, "values": points})
			}
		} else {
			for key, v := range p.instant {
				if contains(query, key) {
					result = append(result, map[string]any{"metric": map[string]string{}, "value": []any{float64(1700000000), v}})
					break
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": result},
		})
	}))
	t.Cleanup(p.Close)
	return p
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestPrometheusReadBuildsCardsSeriesAndReplicas covers the whole read:
// CPU is normalized against the pods' own budget, memory is summed bytes,
// GPU is the exporter's percentage, and the replica table carries one row
// per pod.
func TestPrometheusReadBuildsCardsSeriesAndReplicas(t *testing.T) {
	prom := newPromServer(t,
		map[string]string{
			"container_cpu_usage_seconds_total":  "0.25",
			"container_memory_working_set_bytes": "1073741824",
			"GPU_UTIL":                           "42",
		},
		map[string]string{"GPU_UTIL": "1"},
	)
	svc := New(nil)
	svc.SetPrometheus(prom.URL, "taas-infer", "GPU_UTIL", time.Second)
	svc.SetPodLister(fakePodLister{pods: []ServicePod{{
		Name: "svc-abc", ReplicaIndex: "replica-1",
		CPURequestMilli: 500, CPULimitMilli: 1000,
	}}})

	resp, err := svc.prometheusRead(context.Background(), "svc-1", 1700000000, 1700000120, 60, "")
	require.NoError(t, err)

	require.NotEmpty(t, resp.GetSeries())
	last := resp.GetSeries()[len(resp.GetSeries())-1]
	// 0.25 cores of a 1-core limit is 25%.
	assert.InDelta(t, 25, last.GetCpuPercent(), 0.01)
	assert.EqualValues(t, 1073741824, last.GetMemoryBytes())
	assert.InDelta(t, 42, last.GetGpuPercent(), 0.01)

	cards := resp.GetCards()
	assert.InDelta(t, 25, cards.GetCurrentCpuPercent(), 0.01)
	assert.True(t, cards.GetGpuAvailable(), "an exporter that reports a value means GPU is available")

	require.Len(t, resp.GetReplicas(), 1)
	assert.Equal(t, "replica-1", resp.GetReplicas()[0].GetReplicaIndex())
	assert.InDelta(t, 25, resp.GetReplicas()[0].GetCurrentCpuPercent(), 0.01)
}

// TestPrometheusReadReportsIdleGPUAsAvailable is the bug this fixes: an
// idle GPU reports 0, and the page used to call that "unavailable".
func TestPrometheusReadReportsIdleGPUAsAvailable(t *testing.T) {
	prom := newPromServer(t,
		map[string]string{
			"container_cpu_usage_seconds_total":  "0.1",
			"container_memory_working_set_bytes": "1024",
			"GPU_UTIL":                           "0",
		},
		map[string]string{"GPU_UTIL": "1"},
	)
	svc := New(nil)
	svc.SetPrometheus(prom.URL, "taas-infer", "GPU_UTIL", time.Second)
	svc.SetPodLister(fakePodLister{pods: []ServicePod{{Name: "svc-abc", ReplicaIndex: "replica-1"}}})

	resp, err := svc.prometheusRead(context.Background(), "svc-1", 1700000000, 1700000120, 60, "")
	require.NoError(t, err)
	assert.True(t, resp.GetCards().GetGpuAvailable())
	assert.InDelta(t, 0, resp.GetCards().GetCurrentGpuPercent(), 0.0001)
}

// TestPrometheusReadWithoutExporterMarksGPUUnavailable keeps "not
// measurable" distinct from "idle".
func TestPrometheusReadWithoutExporterMarksGPUUnavailable(t *testing.T) {
	prom := newPromServer(t,
		map[string]string{
			"container_cpu_usage_seconds_total":  "0.1",
			"container_memory_working_set_bytes": "1024",
		},
		nil, // no GPU series at all
	)
	svc := New(nil)
	svc.SetPrometheus(prom.URL, "taas-infer", "GPU_UTIL", time.Second)
	svc.SetPodLister(fakePodLister{pods: []ServicePod{{Name: "svc-abc", ReplicaIndex: "replica-1"}}})

	resp, err := svc.prometheusRead(context.Background(), "svc-1", 1700000000, 1700000120, 60, "")
	require.NoError(t, err)
	assert.False(t, resp.GetCards().GetGpuAvailable())
}

// TestPrometheusReadWithoutPodsIsEmptyNotAnError keeps a stopped service
// from rendering an error page.
func TestPrometheusReadWithoutPodsIsEmptyNotAnError(t *testing.T) {
	svc := New(nil)
	svc.SetPrometheus("http://127.0.0.1:1", "taas-infer", "", time.Second)
	svc.SetPodLister(fakePodLister{})

	resp, err := svc.prometheusRead(context.Background(), "svc-1", 1700000000, 1700000120, 60, "")
	require.NoError(t, err)
	assert.Empty(t, resp.GetSeries())
	assert.Empty(t, resp.GetReplicas())
}

// TestPrometheusSourceQuotesPodNames keeps a pod name from being treated
// as a regex: an unescaped dot would match another pod.
func TestPrometheusSourceQuotesPodNames(t *testing.T) {
	// A dot is a regex metacharacter and must be escaped; a hyphen is
	// literal in RE2 and stays as-is.
	assert.Equal(t, `pod=~"a\.b\.c|x-1"`, podSelector([]string{"a.b.c", "x-1"}))
	assert.Equal(t, "", podSelector(nil))
}

// TestRateWindowBounds the rate() window: it must stay inside the step so
// consecutive points do not overlap, and be long enough to smooth a gap.
func TestRateWindowBounds(t *testing.T) {
	assert.Equal(t, "1m0s", rateWindow(10*time.Second))
	assert.Equal(t, "5m0s", rateWindow(30*time.Minute))
	assert.Equal(t, "2m0s", rateWindow(2*time.Minute))
}

// TestSumByTimestampMergesPods proves a multi-replica service becomes one
// line rather than one line per pod.
func TestSumByTimestampMergesPods(t *testing.T) {
	merged := sumByTimestamp(map[string][]promSample{
		"a": {{timestamp: 2, value: 1}, {timestamp: 1, value: 2}},
		"b": {{timestamp: 1, value: 3}, {timestamp: 2, value: 4}},
	})
	require.Len(t, merged, 2)
	assert.Equal(t, int64(1), merged[0].timestamp)
	assert.InDelta(t, 5, merged[0].value, 0.001)
	assert.Equal(t, int64(2), merged[1].timestamp)
	assert.InDelta(t, 5, merged[1].value, 0.001)
}

// TestPrometheusReadRangeFilter keeps the replica filter working.
func TestPrometheusReadRangeFilter(t *testing.T) {
	prom := newPromServer(t, map[string]string{"cpu_usage": "0.1", "working_set": "1024"}, nil)
	svc := New(nil)
	svc.SetPrometheus(prom.URL, "taas-infer", "GPU_UTIL", time.Second)
	svc.SetPodLister(fakePodLister{pods: []ServicePod{
		{Name: "svc-a", ReplicaIndex: "replica-1"},
		{Name: "svc-b", ReplicaIndex: "replica-2"},
	}})

	resp, err := svc.prometheusRead(context.Background(), "svc-1", 1700000000, 1700000120, 60, "replica-2")
	require.NoError(t, err)
	require.Len(t, resp.GetReplicas(), 1)
	assert.Equal(t, "replica-2", resp.GetReplicas()[0].GetReplicaIndex())
}
