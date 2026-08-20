package main

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Prometheus exposition without a client library.
//
// The text format is a documented, stable, line-oriented format - not a
// protocol - so serving it correctly is about fifty lines. That trade buys a
// dependency-free build and a ~10MB image. It is the right call for a service
// with two endpoints; when this grows histograms per dependency, exemplars or
// native histograms, swap in prometheus/client_golang and delete this file.
//
// Buckets are chosen for a service expected to answer in single-digit
// milliseconds. The default client library buckets start at 5ms, which would
// put almost every request of this service into the first bucket and make the
// histogram useless for spotting a regression from 1ms to 4ms.
var buckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type seriesKey struct {
	method string
	route  string
	status int
}

type histogram struct {
	counts []uint64 // per bucket, cumulative computed at render time
	sum    float64
	count  uint64
}

type metrics struct {
	mu        sync.Mutex
	requests  map[seriesKey]uint64
	durations map[seriesKey]*histogram

	version    string
	workstream string
	owner      string
	started    time.Time
}

func newMetrics(version string) *metrics {
	return &metrics{
		requests:  make(map[seriesKey]uint64),
		durations: make(map[seriesKey]*histogram),
		version:   version,
		// Sourced from the environment so the Deployment sets them once,
		// alongside the u25c.io/* object labels Kyverno requires. Hardcoding
		// them here would mean two places to change and one of them forgotten.
		workstream: getenv("U25C_WORKSTREAM", "argocd"),
		owner:      getenv("U25C_OWNER", "unset"),
		started:    time.Now(),
	}
}

func (m *metrics) observe(method, route string, status int, d time.Duration) {
	k := seriesKey{method: method, route: route, status: status}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests[k]++

	h, ok := m.durations[k]
	if !ok {
		h = &histogram{counts: make([]uint64, len(buckets))}
		m.durations[k] = h
	}

	secs := d.Seconds()
	h.sum += secs
	h.count++

	// Increment exactly one bucket - the first whose upper bound this
	// observation falls under - and let the renderer accumulate. Incrementing
	// every matching bucket here would store cumulative counts, which the
	// renderer would then accumulate a second time.
	//
	// An observation slower than the last bound lands in no bucket at all. That
	// is correct: it is counted only by +Inf, which is what +Inf is for.
	for i, b := range buckets {
		if secs <= b {
			h.counts[i]++
			break
		}
	}
}

func (m *metrics) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder

	// build_info is the conventional way to expose version and ownership: a
	// gauge that is always 1, carrying the detail as labels. Putting version on
	// every series instead would double the time series count on every deploy.
	b.WriteString("# HELP storefront_build_info Build and ownership metadata; always 1.\n")
	b.WriteString("# TYPE storefront_build_info gauge\n")
	fmt.Fprintf(&b, "storefront_build_info{version=\"%s\",workstream=\"%s\",owner=\"%s\",goversion=\"%s\",arch=\"%s\"} 1\n",
		esc(m.version), esc(m.workstream), esc(m.owner), esc(runtime.Version()), esc(runtime.GOARCH))

	b.WriteString("\n# HELP storefront_uptime_seconds Seconds since process start.\n")
	b.WriteString("# TYPE storefront_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "storefront_uptime_seconds %s\n", fl(time.Since(m.started).Seconds()))

	b.WriteString("\n# HELP storefront_http_requests_total Total HTTP requests handled.\n")
	b.WriteString("# TYPE storefront_http_requests_total counter\n")
	for _, k := range sortedKeys(m.requests) {
		fmt.Fprintf(&b, "storefront_http_requests_total{method=\"%s\",route=\"%s\",status=\"%s\"} %d\n",
			esc(k.method), esc(k.route), strconv.Itoa(k.status), m.requests[k])
	}

	b.WriteString("\n# HELP storefront_http_request_duration_seconds Request latency.\n")
	b.WriteString("# TYPE storefront_http_request_duration_seconds histogram\n")
	for _, k := range sortedHistKeys(m.durations) {
		h := m.durations[k]
		labels := fmt.Sprintf(`method="%s",route="%s",status="%s"`, esc(k.method), esc(k.route), strconv.Itoa(k.status))

		// Buckets must be cumulative and must include +Inf, which equals the
		// observation count. A histogram missing +Inf is silently rejected.
		var cumulative uint64
		for i, bound := range buckets {
			cumulative += h.counts[i]
			fmt.Fprintf(&b, "storefront_http_request_duration_seconds_bucket{%s,le=\"%s\"} %d\n",
				labels, fl(bound), cumulative)
		}
		fmt.Fprintf(&b, "storefront_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, h.count)
		fmt.Fprintf(&b, "storefront_http_request_duration_seconds_sum{%s} %s\n", labels, fl(h.sum))
		fmt.Fprintf(&b, "storefront_http_request_duration_seconds_count{%s} %d\n", labels, h.count)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// counts[i] above holds observations falling in bucket i only; the renderer
// accumulates them. Keeping the stored form non-cumulative means observe() does
// one increment rather than walking every remaining bucket.

func sortedKeys(m map[seriesKey]uint64) []seriesKey {
	out := make([]seriesKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortKeys(out)
	return out
}

func sortedHistKeys(m map[seriesKey]*histogram) []seriesKey {
	out := make([]seriesKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortKeys(out)
	return out
}

// Stable ordering. Prometheus does not require it, but a /metrics page whose
// line order changes between scrapes is miserable to diff by hand.
func sortKeys(keys []seriesKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].status < keys[j].status
	})
}

// fl formats a float the way Prometheus expects: no exponent for ordinary
// magnitudes, no trailing zeros.
func fl(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// esc escapes a label value per the exposition format: backslash, double quote
// and newline. Route labels come from registered patterns and are safe today,
// but owner and workstream come from the environment and are not.
func esc(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}
