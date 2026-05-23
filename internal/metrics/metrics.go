package metrics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const maxLatencySamples = 2048

type Event struct {
	Category string
	Name     string
	Value    float64
	Unit     string
	Labels   map[string]string
}

type LatencySnapshot struct {
	Count int     `json:"count"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
}

type ErrorSnapshot struct {
	Count int64 `json:"count"`
}

type CounterSnapshot struct {
	Count int64 `json:"count"`
}

type CacheSnapshot struct {
	Lookups int64   `json:"lookups"`
	Hits    int64   `json:"hits"`
	Misses  int64   `json:"misses"`
	Errors  int64   `json:"errors"`
	HitRate float64 `json:"hit_rate"`
}

type Snapshot struct {
	GeneratedAt    time.Time                  `json:"generated_at"`
	UptimeSeconds  float64                    `json:"uptime_seconds"`
	ThroughputRPS  float64                    `json:"throughput_rps_60s"`
	Latency        map[string]LatencySnapshot `json:"latency"`
	Errors         map[string]ErrorSnapshot   `json:"errors"`
	Counters       map[string]CounterSnapshot `json:"counters"`
	Cache          CacheSnapshot              `json:"cache"`
	BusinessMetric map[string]float64         `json:"business_metric"`
	Runtime        RuntimeSnapshot            `json:"runtime"`
	Resources      map[string]any             `json:"resources,omitempty"`
}

type RuntimeSnapshot struct {
	Goroutines     int    `json:"goroutines"`
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	HeapSysBytes   uint64 `json:"heap_sys_bytes"`
	HeapObjects    uint64 `json:"heap_objects"`
	NumGC          uint32 `json:"num_gc"`
}

type latencySeries struct {
	samples []float64
	next    int
	total   int
}

type collector struct {
	mu             sync.Mutex
	startedAt      time.Time
	latency        map[string]*latencySeries
	errors         map[string]int64
	counters       map[string]int64
	requests       []time.Time
	cacheLookups   int64
	cacheHits      int64
	cacheMisses    int64
	cacheErrors    int64
	businessValues map[string]float64
}

var (
	defaultCollector = &collector{
		startedAt:      time.Now(),
		latency:        map[string]*latencySeries{},
		errors:         map[string]int64{},
		counters:       map[string]int64{},
		businessValues: map[string]float64{},
	}
	sinkMu            sync.RWMutex
	eventSink         func(Event)
	resourceCollector func() map[string]any
)

func SetEventSink(sink func(Event)) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	eventSink = sink
}

func SetResourceCollector(collector func() map[string]any) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	resourceCollector = collector
}

func ObserveLatency(layer string, duration time.Duration, labels map[string]string) {
	ms := float64(duration.Microseconds()) / 1000

	defaultCollector.mu.Lock()
	series := defaultCollector.latency[layer]
	if series == nil {
		series = &latencySeries{samples: make([]float64, 0, maxLatencySamples)}
		defaultCollector.latency[layer] = series
	}
	if len(series.samples) < maxLatencySamples {
		series.samples = append(series.samples, ms)
	} else {
		series.samples[series.next] = ms
		series.next = (series.next + 1) % maxLatencySamples
	}
	series.total++
	defaultCollector.mu.Unlock()

	emit(Event{Category: "latency", Name: layer, Value: ms, Unit: "ms", Labels: labels})
}

func IncCounter(name string, labels map[string]string) {
	defaultCollector.mu.Lock()
	defaultCollector.counters[name]++
	defaultCollector.mu.Unlock()

	emit(Event{Category: "counter", Name: name, Value: 1, Unit: "count", Labels: labels})
}

func IncError(errorType string, labels map[string]string) {
	defaultCollector.mu.Lock()
	defaultCollector.errors[errorType]++
	defaultCollector.mu.Unlock()

	emit(Event{Category: "error", Name: errorType, Value: 1, Unit: "count", Labels: labels})
}

func RecordHTTPRequest(route string, statusCode int, duration time.Duration) {
	labels := map[string]string{
		"route":  route,
		"status": strconv.Itoa(statusCode),
	}
	ObserveLatency("http.request", duration, labels)
	IncCounter("http.requests", labels)

	now := time.Now()
	cutoff := now.Add(-time.Minute)
	defaultCollector.mu.Lock()
	defaultCollector.requests = append(defaultCollector.requests, now)
	firstRecent := 0
	for firstRecent < len(defaultCollector.requests) && defaultCollector.requests[firstRecent].Before(cutoff) {
		firstRecent++
	}
	if firstRecent > 0 {
		copy(defaultCollector.requests, defaultCollector.requests[firstRecent:])
		defaultCollector.requests = defaultCollector.requests[:len(defaultCollector.requests)-firstRecent]
	}
	defaultCollector.mu.Unlock()
}

func RecordCacheLookup(hit bool, hadError bool, labels map[string]string) {
	defaultCollector.mu.Lock()
	defaultCollector.cacheLookups++
	switch {
	case hadError:
		defaultCollector.cacheErrors++
	case hit:
		defaultCollector.cacheHits++
	default:
		defaultCollector.cacheMisses++
	}
	defaultCollector.mu.Unlock()

	status := "miss"
	if hadError {
		status = "error"
	} else if hit {
		status = "hit"
	}
	allLabels := cloneLabels(labels)
	allLabels["status"] = status
	emit(Event{Category: "cache", Name: "semantic_cache_lookup", Value: 1, Unit: "count", Labels: allLabels})
}

func AddBusinessValue(name string, value float64, unit string, labels map[string]string) {
	if value == 0 {
		return
	}
	defaultCollector.mu.Lock()
	defaultCollector.businessValues[name] += value
	defaultCollector.mu.Unlock()

	emit(Event{Category: "business", Name: name, Value: value, Unit: unit, Labels: labels})
}

func CurrentSnapshot() Snapshot {
	now := time.Now()

	defaultCollector.mu.Lock()
	latency := make(map[string]LatencySnapshot, len(defaultCollector.latency))
	for name, series := range defaultCollector.latency {
		latency[name] = snapshotLatency(series)
	}
	errors := make(map[string]ErrorSnapshot, len(defaultCollector.errors))
	for name, count := range defaultCollector.errors {
		errors[name] = ErrorSnapshot{Count: count}
	}
	counters := make(map[string]CounterSnapshot, len(defaultCollector.counters))
	for name, count := range defaultCollector.counters {
		counters[name] = CounterSnapshot{Count: count}
	}
	business := make(map[string]float64, len(defaultCollector.businessValues))
	for name, value := range defaultCollector.businessValues {
		business[name] = value
	}
	cache := CacheSnapshot{
		Lookups: defaultCollector.cacheLookups,
		Hits:    defaultCollector.cacheHits,
		Misses:  defaultCollector.cacheMisses,
		Errors:  defaultCollector.cacheErrors,
	}
	if cache.Lookups > 0 {
		cache.HitRate = float64(cache.Hits) / float64(cache.Lookups)
	}
	rps := float64(len(defaultCollector.requests)) / 60
	startedAt := defaultCollector.startedAt
	defaultCollector.mu.Unlock()

	snapshot := Snapshot{
		GeneratedAt:    now,
		UptimeSeconds:  now.Sub(startedAt).Seconds(),
		ThroughputRPS:  rps,
		Latency:        latency,
		Errors:         errors,
		Counters:       counters,
		Cache:          cache,
		BusinessMetric: business,
		Runtime:        runtimeSnapshot(),
	}

	sinkMu.RLock()
	if resourceCollector != nil {
		snapshot.Resources = resourceCollector()
	}
	sinkMu.RUnlock()

	return snapshot
}

func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(CurrentSnapshot())
}

func PrometheusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	WritePrometheus(w, CurrentSnapshot())
}

func WritePrometheus(w http.ResponseWriter, snapshot Snapshot) {
	writePromMetric(w, "llm_gateway_uptime_seconds", "gauge", "Process uptime in seconds.", nil, snapshot.UptimeSeconds)
	writePromMetric(w, "llm_gateway_throughput_rps_60s", "gauge", "Requests per second over the last 60 seconds.", nil, snapshot.ThroughputRPS)

	writePromHeader(w, "llm_gateway_latency_samples_total", "counter", "Latency samples observed by layer.")
	writePromHeader(w, "llm_gateway_latency_p50_ms", "gauge", "Rolling p50 latency by layer in milliseconds.")
	writePromHeader(w, "llm_gateway_latency_p95_ms", "gauge", "Rolling p95 latency by layer in milliseconds.")
	writePromHeader(w, "llm_gateway_latency_p99_ms", "gauge", "Rolling p99 latency by layer in milliseconds.")
	for layer, latency := range snapshot.Latency {
		labels := map[string]string{"layer": layer}
		writePromSample(w, "llm_gateway_latency_samples_total", labels, float64(latency.Count))
		writePromSample(w, "llm_gateway_latency_p50_ms", labels, latency.P50Ms)
		writePromSample(w, "llm_gateway_latency_p95_ms", labels, latency.P95Ms)
		writePromSample(w, "llm_gateway_latency_p99_ms", labels, latency.P99Ms)
	}

	for name, counter := range snapshot.Counters {
		writePromMetric(w, "llm_gateway_"+metricName(name)+"_total", "counter", "Application counter.", nil, float64(counter.Count))
	}
	writePromHeader(w, "llm_gateway_errors_total", "counter", "Application errors by type.")
	for name, err := range snapshot.Errors {
		writePromSample(w, "llm_gateway_errors_total", map[string]string{"type": name}, float64(err.Count))
	}

	writePromMetric(w, "llm_gateway_cache_lookups_total", "counter", "Semantic cache lookups.", nil, float64(snapshot.Cache.Lookups))
	writePromMetric(w, "llm_gateway_cache_hits_total", "counter", "Semantic cache hits.", nil, float64(snapshot.Cache.Hits))
	writePromMetric(w, "llm_gateway_cache_misses_total", "counter", "Semantic cache misses.", nil, float64(snapshot.Cache.Misses))
	writePromMetric(w, "llm_gateway_cache_errors_total", "counter", "Semantic cache errors.", nil, float64(snapshot.Cache.Errors))
	writePromMetric(w, "llm_gateway_cache_hit_rate", "gauge", "Semantic cache hit rate.", nil, snapshot.Cache.HitRate)

	for name, value := range snapshot.BusinessMetric {
		writePromMetric(w, "llm_gateway_business_"+metricName(name), "counter", "Business metric.", nil, value)
	}

	writePromMetric(w, "llm_gateway_go_goroutines", "gauge", "Current number of goroutines.", nil, float64(snapshot.Runtime.Goroutines))
	writePromMetric(w, "llm_gateway_go_heap_alloc_bytes", "gauge", "Bytes of allocated heap objects.", nil, float64(snapshot.Runtime.HeapAllocBytes))
	writePromMetric(w, "llm_gateway_go_heap_sys_bytes", "gauge", "Bytes of heap memory obtained from the OS.", nil, float64(snapshot.Runtime.HeapSysBytes))
	writePromMetric(w, "llm_gateway_go_heap_objects", "gauge", "Number of allocated heap objects.", nil, float64(snapshot.Runtime.HeapObjects))
	writePromMetric(w, "llm_gateway_go_gc_runs_total", "counter", "Completed GC cycles.", nil, float64(snapshot.Runtime.NumGC))
}

func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		RecordHTTPRequest(r.URL.Path, rec.statusCode, time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func snapshotLatency(series *latencySeries) LatencySnapshot {
	values := append([]float64(nil), series.samples...)
	sort.Float64s(values)
	return LatencySnapshot{
		Count: series.total,
		P50Ms: percentile(values, 0.50),
		P95Ms: percentile(values, 0.95),
		P99Ms: percentile(values, 0.99),
	}
}

func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(q*float64(len(values)-1) + 0.5)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func emit(event Event) {
	sinkMu.RLock()
	sink := eventSink
	sinkMu.RUnlock()
	if sink != nil {
		go sink(event)
	}
}

func cloneLabels(labels map[string]string) map[string]string {
	cloned := map[string]string{}
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func runtimeSnapshot() RuntimeSnapshot {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return RuntimeSnapshot{
		Goroutines:     runtime.NumGoroutine(),
		HeapAllocBytes: mem.HeapAlloc,
		HeapSysBytes:   mem.HeapSys,
		HeapObjects:    mem.HeapObjects,
		NumGC:          mem.NumGC,
	}
}

func writePromMetric(w http.ResponseWriter, name string, metricType string, help string, labels map[string]string, value float64) {
	writePromHeader(w, name, metricType, help)
	writePromSample(w, name, labels, value)
}

func writePromHeader(w http.ResponseWriter, name string, metricType string, help string) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s %s\n", name, metricType)
}

func writePromSample(w http.ResponseWriter, name string, labels map[string]string, value float64) {
	fmt.Fprintf(w, "%s%s %g\n", name, formatLabels(labels), value)
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, metricName(key), escapeLabelValue(labels[key])))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func metricName(value string) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		return "unknown"
	}
	if name[0] >= '0' && name[0] <= '9' {
		return "_" + name
	}
	return name
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
