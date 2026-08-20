package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestApp() *app {
	return newApp("test-1.0.0", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(t *testing.T, a *app, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestProductsListed(t *testing.T) {
	rec := do(t, newTestApp(), http.MethodGet, "/api/products")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		Products []Product `json:"products"`
		Count    int       `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != len(body.Products) {
		t.Errorf("count = %d but %d products returned", body.Count, len(body.Products))
	}
	if body.Count == 0 {
		t.Error("catalogue is empty")
	}
}

func TestProductByID(t *testing.T) {
	a := newTestApp()

	t.Run("found", func(t *testing.T) {
		rec := do(t, a, http.MethodGet, "/api/products/sku-1001")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var p Product
		if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.ID != "sku-1001" {
			t.Errorf("id = %q, want sku-1001", p.ID)
		}
	})

	t.Run("missing", func(t *testing.T) {
		rec := do(t, a, http.MethodGet, "/api/products/nope")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

// Readiness must fail once draining has begun, otherwise the pod keeps
// receiving traffic while it is shutting down. This is the behaviour the
// graceful-shutdown path in main() depends on.
func TestReadinessFlipsWhenDraining(t *testing.T) {
	a := newTestApp()

	if rec := do(t, a, http.MethodGet, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("fresh app: status = %d, want 200", rec.Code)
	}

	a.setReady(false)

	if rec := do(t, a, http.MethodGet, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("draining app: status = %d, want 503", rec.Code)
	}
}

// Liveness must NOT follow readiness. If it did, kubelet would restart a pod
// that is deliberately draining and turn a clean rollout into a crash loop.
func TestLivenessIgnoresDraining(t *testing.T) {
	a := newTestApp()
	a.setReady(false)

	if rec := do(t, a, http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 while draining", rec.Code)
	}
}

// Path parameters must not leak into metric labels, or every product id
// becomes its own time series.
func TestRouteLabelIsPatternNotPath(t *testing.T) {
	a := newTestApp()

	do(t, a, http.MethodGet, "/api/products/sku-1001")
	do(t, a, http.MethodGet, "/api/products/sku-1002")

	body := do(t, a, http.MethodGet, "/metrics").Body.String()

	if strings.Contains(body, "sku-1001") || strings.Contains(body, "sku-1002") {
		t.Error("product id leaked into metric labels — unbounded cardinality")
	}
	if !strings.Contains(body, `route="/api/products/{id}"`) {
		t.Errorf("expected the route pattern as the label, got:\n%s", body)
	}
}

// Probe traffic is constant; letting it into the request histogram would swamp
// the real signal.
func TestProbesAreNotInstrumented(t *testing.T) {
	a := newTestApp()

	do(t, a, http.MethodGet, "/healthz")
	do(t, a, http.MethodGet, "/readyz")

	body := do(t, a, http.MethodGet, "/metrics").Body.String()

	if strings.Contains(body, `route="/healthz"`) || strings.Contains(body, `route="/readyz"`) {
		t.Error("probe endpoints were recorded in request metrics")
	}
}

func TestMetricsExpositionShape(t *testing.T) {
	a := newTestApp()
	a.metrics.observe(http.MethodGet, "/api/products", 200, 3*time.Millisecond)

	body := do(t, a, http.MethodGet, "/metrics").Body.String()

	// A histogram without +Inf is silently rejected by Prometheus.
	if !strings.Contains(body, `le="+Inf"`) {
		t.Error("histogram is missing the +Inf bucket")
	}
	for _, want := range []string{
		"# TYPE storefront_http_request_duration_seconds histogram",
		"storefront_http_request_duration_seconds_sum",
		"storefront_http_request_duration_seconds_count",
		"storefront_build_info",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
}

// Buckets are cumulative: each must be >= the one before it. Getting this wrong
// produces a histogram that renders but whose quantiles are nonsense.
func TestHistogramBucketsAreCumulative(t *testing.T) {
	a := newTestApp()
	for _, d := range []time.Duration{500 * time.Microsecond, 3 * time.Millisecond, 200 * time.Millisecond} {
		a.metrics.observe(http.MethodGet, "/api/products", 200, d)
	}

	body := do(t, a, http.MethodGet, "/metrics").Body.String()

	var prev uint64
	var seen int
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "storefront_http_request_duration_seconds_bucket") {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseUint(fields[len(fields)-1], 10, 64)
		if err != nil {
			t.Fatalf("parsing %q: %v", line, err)
		}
		if v < prev {
			t.Errorf("bucket count went backwards: %d after %d\n%s", v, prev, line)
		}
		prev = v
		seen++
	}
	if seen != len(buckets)+1 {
		t.Errorf("got %d bucket lines, want %d", seen, len(buckets)+1)
	}
	if prev != 3 {
		t.Errorf("+Inf bucket = %d, want 3 (one per observation)", prev)
	}
}
