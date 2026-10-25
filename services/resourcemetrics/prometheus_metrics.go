package resourcemetrics

import (
	"context"
	"strconv"
	"strings"
	"time"

	resourcemetricsv1 "github.com/go-taas/go-taas/proto/taas/resourcemetrics/v1"
)

// ServicePod is what the metrics read needs to know about one running
// pod: its name (the Prometheus join key), the masked replica index the
// other pages use, and the CPU budget its percent is relative to.
type ServicePod struct {
	// Name is the real pod name.
	Name string
	// ReplicaIndex is the masked label ("replica-1"), matching the log
	// viewer so one pod has one name across the console.
	ReplicaIndex string
	// CPURequestMilli is the summed container CPU request, or 0.
	CPURequestMilli int64
	// CPULimitMilli is the summed container CPU limit, or 0.
	CPULimitMilli int64
	// MemoryLimitBytes is the summed container memory limit, or 0.
	MemoryLimitBytes int64
}

// PodLister lists a service's running pods. It is implemented in the
// server wiring over the Kubernetes client, which already holds the
// permission, and is injected so this module needs no cluster access.
type PodLister interface {
	ServicePods(ctx context.Context, serviceID string) ([]ServicePod, error)
}

// SetPodLister injects the pod lister used by the Prometheus read path.
func (s *Service) SetPodLister(l PodLister) { s.podLister = l }

// SetPrometheus configures the Prometheus read path. An empty url leaves
// the stored-sample path in place.
func (s *Service) SetPrometheus(baseURL, namespace, gpuMetric string, timeout time.Duration) {
	s.prometheus = newPrometheusSource(baseURL, namespace, gpuMetric, timeout)
}

// prometheusRead builds the whole response from Prometheus.
//
// Three range queries cover the page — CPU, memory and GPU — each grouped
// by pod, so the service-wide line is their sum and the replica table is
// their last point. GPU availability is one extra instant query: an idle
// GPU reports 0 while a cluster without a GPU exporter reports nothing,
// and the page has to tell those apart.
func (s *Service) prometheusRead(ctx context.Context, serviceID string, since, until int64, bucketSize int64, replica string) (*resourcemetricsv1.GetServiceResourceMetricsResponse, error) {
	pods, err := s.podLister.ServicePods(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	if replica != "" {
		pods = filterServicePods(pods, replica)
	}
	if len(pods) == 0 {
		// No running pod: an empty page is the honest answer, not an error.
		return &resourcemetricsv1.GetServiceResourceMetricsResponse{
			Response: okResponse(),
			Cards:    &resourcemetricsv1.ResourceMetricsCard{DataThrough: until},
		}, nil
	}

	names := make([]string, 0, len(pods))
	cpuBudgetMilli := map[string]float64{}
	for _, pod := range pods {
		names = append(names, pod.Name)
		// CPU percent is relative to the pod's limit, else its request,
		// else one core: a limit is what "busy" means for a throttled
		// container, and one core keeps an unconfigured pod readable.
		budget := pod.CPULimitMilli
		if budget <= 0 {
			budget = pod.CPURequestMilli
		}
		if budget <= 0 {
			budget = 1000
		}
		cpuBudgetMilli[pod.Name] = float64(budget)
	}
	selector := podSelector(names)
	step := time.Duration(bucketSize) * time.Second

	cpuQuery := `sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="` + s.prometheus.namespace +
		`",container!="",container!="POD",` + selector + `}[` + rateWindow(step) + `]))`
	memQuery := `sum by (pod) (container_memory_working_set_bytes{namespace="` + s.prometheus.namespace +
		`",container!="",container!="POD",` + selector + `})`
	gpuQuery := `avg by (pod) (` + s.prometheus.gpuMetric + `{` + selector + `})`

	cpuByPod, err := s.prometheus.queryRangeByPod(ctx, cpuQuery, since, until, step)
	if err != nil {
		return nil, err
	}
	memByPod, err := s.prometheus.queryRangeByPod(ctx, memQuery, since, until, step)
	if err != nil {
		return nil, err
	}
	gpuByPod, err := s.prometheus.queryRangeByPod(ctx, gpuQuery, since, until, step)
	if err != nil {
		// A cluster without a GPU exporter must not break the page: the
		// CPU and memory numbers are still useful.
		warnPrometheus(err)
		gpuByPod = nil
	}

	// Service-wide series: sum across pods per timestamp, then normalize
	// CPU to a percentage of the pods' combined budget.
	cpuSeries := sumByTimestamp(cpuByPod)
	memSeries := sumByTimestamp(memByPod)
	gpuSeries := sumByTimestamp(gpuByPod)

	totalBudget := 0.0
	for _, budget := range cpuBudgetMilli {
		totalBudget += budget
	}

	series := make([]*resourcemetricsv1.ResourceMetricsSeriesPoint, 0, len(cpuSeries))
	for i := range cpuSeries {
		point := &resourcemetricsv1.ResourceMetricsSeriesPoint{Bucket: cpuSeries[i].timestamp}
		if totalBudget > 0 {
			// rate() yields cores; the budget is in millicores.
			point.CpuPercent = cpuSeries[i].value / (totalBudget / 1000) * 100
		}
		if i < len(memSeries) {
			point.MemoryBytes = int64(memSeries[i].value)
		}
		if i < len(gpuSeries) {
			point.GpuPercent = gpuSeries[i].value
		}
		series = append(series, point)
	}

	// Per-replica rows: each pod's last point.
	replicaRows := make([]*resourcemetricsv1.ResourceMetricsReplicaRow, 0, len(pods))
	for _, pod := range pods {
		row := &resourcemetricsv1.ResourceMetricsReplicaRow{ReplicaIndex: pod.ReplicaIndex}
		if last := lastPoint(cpuByPod[pod.Name]); last != nil {
			// A per-pod percentage is relative to that pod's own budget,
			// and rate() yields cores while the budget is millicores.
			if budget := cpuBudgetMilli[pod.Name]; budget > 0 {
				row.CurrentCpuPercent = last.value / (budget / 1000) * 100
			}
			row.DataThrough = last.timestamp
		}
		if last := lastPoint(memByPod[pod.Name]); last != nil {
			row.CurrentMemoryBytes = int64(last.value)
			if last.timestamp > row.DataThrough {
				row.DataThrough = last.timestamp
			}
		}
		if last := lastPoint(gpuByPod[pod.Name]); last != nil {
			row.CurrentGpuPercent = last.value
			if last.timestamp > row.DataThrough {
				row.DataThrough = last.timestamp
			}
		}
		replicaRows = append(replicaRows, row)
	}

	// Engine and card-memory telemetry sits alongside utilization: TTFT and
	// TPOT are what an operator sizes a deployment by, and they only exist
	// in the engine's own metrics.
	telemetry := s.readEngineTelemetry(ctx, selector, since, until, step)
	mergeEngineSeries(series, telemetry)

	cards := buildPrometheusCards(series, replicaRows)
	cards.GpuAvailable = s.gpuAvailable(ctx, selector)
	applyEngineCards(cards, telemetry)
	return &resourcemetricsv1.GetServiceResourceMetricsResponse{
		Response: okResponse(),
		Cards:    cards,
		Series:   series,
		Replicas: replicaRows,
	}, nil
}

// gpuAvailable reports whether the GPU exporter exposes this service's
// pods at all.
func (s *Service) gpuAvailable(ctx context.Context, selector string) bool {
	count, err := s.prometheus.queryCount(ctx, `count(`+s.prometheus.gpuMetric+`{`+selector+`})`)
	if err != nil {
		warnPrometheus(err)
		return false
	}
	return count > 0
}

// buildPrometheusCards derives the headline cards from the last point.
func buildPrometheusCards(series []*resourcemetricsv1.ResourceMetricsSeriesPoint, replicas []*resourcemetricsv1.ResourceMetricsReplicaRow) *resourcemetricsv1.ResourceMetricsCard {
	card := &resourcemetricsv1.ResourceMetricsCard{ReplicaCount: int64(len(replicas))}
	if len(series) > 0 {
		last := series[len(series)-1]
		card.CurrentCpuPercent = last.CpuPercent
		card.CurrentMemoryBytes = last.MemoryBytes
		card.CurrentGpuPercent = last.GpuPercent
		card.DataThrough = last.Bucket
	}
	return card
}

// sumByTimestamp merges per-pod series into one series, summing values
// that share a timestamp and keeping the timestamps sorted.
func sumByTimestamp(byPod map[string][]promSample) []promSample {
	if len(byPod) == 0 {
		return nil
	}
	sums := map[int64]float64{}
	for _, points := range byPod {
		for _, point := range points {
			sums[point.timestamp] += point.value
		}
	}
	out := make([]promSample, 0, len(sums))
	for ts, value := range sums {
		out = append(out, promSample{timestamp: ts, value: value})
	}
	sortSamples(out)
	return out
}

// lastPoint returns the newest sample of a series.
func lastPoint(points []promSample) *promSample {
	if len(points) == 0 {
		return nil
	}
	return &points[len(points)-1]
}

// sortSamples orders samples by timestamp (insertion sort: the slices are
// small and this keeps the allocation count at zero).
func sortSamples(samples []promSample) {
	for i := 1; i < len(samples); i++ {
		for j := i; j > 0 && samples[j].timestamp < samples[j-1].timestamp; j-- {
			samples[j], samples[j-1] = samples[j-1], samples[j]
		}
	}
}

// filterServicePods keeps only the pod whose masked index matches.
func filterServicePods(pods []ServicePod, replica string) []ServicePod {
	replica = strings.TrimSpace(replica)
	if replica == "" {
		return pods
	}
	out := make([]ServicePod, 0, 1)
	for _, pod := range pods {
		if pod.ReplicaIndex == replica {
			out = append(out, pod)
		}
	}
	return out
}

// rateWindow picks the rate() window: long enough to smooth a scrape
// gap, short enough to stay inside the step so points do not overlap.
//
// The result is expressed in seconds because Go's Duration.String() emits
// compound units ("5m0s") that PromQL rejects — a malformed window makes
// every range query return nothing, which looked like "no metrics".
func rateWindow(step time.Duration) string {
	window := step
	if window < time.Minute {
		window = time.Minute
	}
	if window > 5*time.Minute {
		window = 5 * time.Minute
	}
	return strconv.Itoa(int(window.Seconds())) + "s"
}

// engineRead is the engine/VRAM telemetry the page shows next to CPU,
// memory and GPU utilization. Any field whose metric is missing stays
// zero, and the availability flags let the console say "not measured"
// rather than print a misleading 0.
type engineRead struct {
	// vramSeries / ttftSeries / tpotSeries / throughputSeries feed the
	// chart, so the same numbers are visible over time rather than only at
	// the last sample.
	vramSeries       []promSample
	ttftSeries       []promSample
	tpotSeries       []promSample
	throughputSeries []promSample

	vramBytes        int64
	vramTotalBytes   int64
	ttftP95Ms        float64
	tpotP95Ms        float64
	kvCachePercent   float64
	requestsRunning  float64
	requestsWaiting  float64
	tokensPerSecond  float64
	gpuMemoryPresent bool
	enginePresent    bool
}

// readEngineTelemetry collects the engine and card-memory numbers.
//
// Each panel is independent: a cluster with DCGM but no engine scrape still
// shows VRAM, and an engine with metrics but no GPU exporter still shows
// TTFT and TPOT. A failure in one query never blanks the others — the page
// is a monitoring view, and half the numbers beat none.
func (s *Service) readEngineTelemetry(ctx context.Context, selector string, since, until int64, step time.Duration) engineRead {
	var out engineRead
	names := s.prometheus.engine
	if names.Quantile <= 0 || names.Quantile > 1 {
		names.Quantile = 0.95
	}
	quantile := strconv.FormatFloat(names.Quantile, 'f', 2, 64)

	// Card memory: DCGM reports MiB, the page works in bytes.
	if s.prometheus.gpuMemoryUsed != "" {
		usedByPod, err := s.prometheus.queryRangeByPod(ctx,
			`sum by (pod) (`+s.prometheus.gpuMemoryUsed+`{`+selector+`})`, since, until, step)
		if err == nil {
			out.vramSeries = scaledSamples(sumByTimestamp(usedByPod), mibToBytes)
			if last := lastPoint(out.vramSeries); last != nil {
				out.vramBytes = int64(last.value)
				out.gpuMemoryPresent = true
			}
		} else {
			warnPrometheus(err)
		}
	}
	if s.prometheus.gpuMemoryTotal != "" {
		// Capacity is not per pod: each card reports its own total, so the
		// sum would multiply by the number of pods sharing a card.
		total, err := s.prometheus.queryScalar(ctx,
			`max(`+s.prometheus.gpuMemoryTotal+`{`+selector+`})`)
		if err == nil && total > 0 {
			out.vramTotalBytes = int64(total) * mibToBytes
		}
	}

	// TTFT and TPOT: percentile over the engine's histograms. The rate
	// window is the query step, so a long range does not average away a
	// spike and a short one still has samples.
	window := rateWindow(step)
	if names.TTFTBucket != "" {
		series, err := s.prometheus.queryRange(
			ctx,
			`histogram_quantile(`+quantile+`, sum by (le) (rate(`+names.TTFTBucket+`{`+selector+`}[`+window+`])))`,
			since, until, step)
		if err == nil {
			out.ttftSeries = scaledSamples(series, secondsToMillis)
			if last := lastPoint(out.ttftSeries); last != nil {
				out.ttftP95Ms = last.value
				out.enginePresent = true
			}
		} else {
			warnPrometheus(err)
		}
	}
	if names.TPOTBucket != "" {
		series, err := s.prometheus.queryRange(
			ctx,
			`histogram_quantile(`+quantile+`, sum by (le) (rate(`+names.TPOTBucket+`{`+selector+`}[`+window+`])))`,
			since, until, step)
		if err == nil {
			out.tpotSeries = scaledSamples(series, secondsToMillis)
			if last := lastPoint(out.tpotSeries); last != nil {
				out.tpotP95Ms = last.value
				out.enginePresent = true
			}
		} else {
			warnPrometheus(err)
		}
	}

	// Output-token throughput.
	if names.GenerationTokens != "" {
		series, err := s.prometheus.queryRange(
			ctx, `sum(rate(`+names.GenerationTokens+`{`+selector+`}[`+window+`]))`, since, until, step)
		if err == nil {
			out.throughputSeries = series
			if last := lastPoint(series); last != nil {
				out.tokensPerSecond = last.value
				out.enginePresent = true
			}
		} else {
			warnPrometheus(err)
		}
	}

	// Gauges: one instant query each, since only the current value is shown.
	if names.KVCacheUsage != "" {
		if value, ok, err := s.prometheus.queryScalarOK(ctx, `avg(`+names.KVCacheUsage+`{`+selector+`})`); err == nil && ok {
			// The engine reports a fraction; the page shows a percentage.
			out.kvCachePercent = value * 100
			out.enginePresent = true
		}
	}
	if names.RequestsRunning != "" {
		if value, ok, err := s.prometheus.queryScalarOK(ctx, `sum(`+names.RequestsRunning+`{`+selector+`})`); err == nil && ok {
			out.requestsRunning = value
			out.enginePresent = true
		}
	}
	if names.RequestsWaiting != "" {
		if value, ok, err := s.prometheus.queryScalarOK(ctx, `sum(`+names.RequestsWaiting+`{`+selector+`})`); err == nil && ok {
			out.requestsWaiting = value
			out.enginePresent = true
		}
	}
	return out
}

// mibToBytes converts DCGM's MiB to bytes.
const mibToBytes = 1024 * 1024

// scaledSamples multiplies every sample by a factor (unit conversion).
func scaledSamples(samples []promSample, factor float64) []promSample {
	out := make([]promSample, 0, len(samples))
	for _, sample := range samples {
		out = append(out, promSample{timestamp: sample.timestamp, value: sample.value * factor})
	}
	return out
}

// secondsToMillis converts a latency in seconds to milliseconds.
const secondsToMillis = 1000.0

// mergeEngineSeries attaches the engine series to the utilization points so
// one bucket carries every metric. The timestamps can differ by a scrape
// interval, so each engine series is matched to its nearest utilization
// bucket rather than requiring an exact timestamp.
func mergeEngineSeries(series []*resourcemetricsv1.ResourceMetricsSeriesPoint, telemetry engineRead) {
	if len(series) == 0 {
		return
	}
	applyToNearest := func(samples []promSample, set func(point *resourcemetricsv1.ResourceMetricsSeriesPoint, value float64)) {
		for _, sample := range samples {
			point := nearestPoint(series, sample.timestamp)
			if point != nil {
				set(point, sample.value)
			}
		}
	}
	applyToNearest(telemetry.vramSeries, func(p *resourcemetricsv1.ResourceMetricsSeriesPoint, v float64) {
		p.GpuMemoryBytes = int64(v)
	})
	applyToNearest(telemetry.ttftSeries, func(p *resourcemetricsv1.ResourceMetricsSeriesPoint, v float64) {
		p.TtftP95Ms = v
	})
	applyToNearest(telemetry.tpotSeries, func(p *resourcemetricsv1.ResourceMetricsSeriesPoint, v float64) {
		p.TpotP95Ms = v
	})
	applyToNearest(telemetry.throughputSeries, func(p *resourcemetricsv1.ResourceMetricsSeriesPoint, v float64) {
		p.TokensPerSecond = v
	})
}

// nearestPoint returns the bucket closest to a timestamp, or nil when the
// series is empty.
func nearestPoint(series []*resourcemetricsv1.ResourceMetricsSeriesPoint, timestamp int64) *resourcemetricsv1.ResourceMetricsSeriesPoint {
	if len(series) == 0 {
		return nil
	}
	best := series[0]
	bestDistance := absInt64(series[0].Bucket - timestamp)
	for _, point := range series[1:] {
		if distance := absInt64(point.Bucket - timestamp); distance < bestDistance {
			best = point
			bestDistance = distance
		}
	}
	return best
}

// absInt64 returns the absolute value.
func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// applyEngineCards fills the engine and VRAM fields on the summary card.
func applyEngineCards(card *resourcemetricsv1.ResourceMetricsCard, telemetry engineRead) {
	card.CurrentGpuMemoryBytes = telemetry.vramBytes
	card.GpuMemoryTotalBytes = telemetry.vramTotalBytes
	card.TtftP95Ms = telemetry.ttftP95Ms
	card.TpotP95Ms = telemetry.tpotP95Ms
	card.KvCachePercent = telemetry.kvCachePercent
	card.RequestsRunning = telemetry.requestsRunning
	card.RequestsWaiting = telemetry.requestsWaiting
	card.TokensPerSecond = telemetry.tokensPerSecond
	card.EngineAvailable = telemetry.enginePresent
}

// SetEngineMetrics overrides the engine and card-memory metric names. The
// defaults are vLLM's and DCGM's; wiring passes `resourceMetrics.*` so a
// different engine is a configuration change.
func (s *Service) SetEngineMetrics(engine EngineMetricNames, memoryUsed, memoryTotal string) {
	if s.prometheus == nil {
		return
	}
	s.prometheus.engine = engine
	s.prometheus.gpuMemoryUsed = memoryUsed
	s.prometheus.gpuMemoryTotal = memoryTotal
}
