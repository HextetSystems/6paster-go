package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type requestMetricKey struct {
	method string
	route  string
	status int
}

type requestMetricValue struct {
	count    uint64
	duration float64
}

type requestMetrics struct {
	mu     sync.Mutex
	values map[requestMetricKey]requestMetricValue
}

func newRequestMetrics() *requestMetrics {
	return &requestMetrics{values: make(map[requestMetricKey]requestMetricValue)}
}

func (m *requestMetrics) observe(method, route string, status int, duration float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := requestMetricKey{method: method, route: route, status: status}
	value := m.values[key]
	value.count++
	value.duration += duration
	m.values[key] = value
}

func (m *requestMetrics) serve(w http.ResponseWriter) {
	m.mu.Lock()
	values := make(map[requestMetricKey]requestMetricValue, len(m.values))
	for key, value := range m.values {
		values[key] = value
	}
	m.mu.Unlock()

	keys := make([]requestMetricKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.method != b.method {
			return a.method < b.method
		}
		if a.route != b.route {
			return a.route < b.route
		}
		return a.status < b.status
	})
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var body strings.Builder
	body.WriteString("# HELP sixpaster_http_requests_total Total HTTP requests.\n# TYPE sixpaster_http_requests_total counter\n")
	body.WriteString("# HELP sixpaster_http_request_duration_seconds_sum Total HTTP request duration in seconds.\n# TYPE sixpaster_http_request_duration_seconds_sum counter\n")
	body.WriteString("# HELP sixpaster_http_request_duration_seconds_count HTTP request duration observations.\n# TYPE sixpaster_http_request_duration_seconds_count counter\n")
	for _, key := range keys {
		value := values[key]
		labels := fmt.Sprintf("method=%q,route=%q,status=%q", key.method, key.route, fmt.Sprint(key.status))
		fmt.Fprintf(&body, "sixpaster_http_requests_total{%s} %d\n", labels, value.count)
		fmt.Fprintf(&body, "sixpaster_http_request_duration_seconds_sum{%s} %g\n", labels, value.duration)
		fmt.Fprintf(&body, "sixpaster_http_request_duration_seconds_count{%s} %d\n", labels, value.count)
	}
	_, _ = w.Write([]byte(body.String()))
}
