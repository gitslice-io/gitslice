//go:build load

package load_test

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
)

func drainDurations(ch <-chan time.Duration) []time.Duration {
	var out []time.Duration
	for d := range ch {
		out = append(out, d)
	}
	sortDurations(out)
	return out
}

func sortDurations(durations []time.Duration) {
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
}

func reportDurations(t *testing.T, name string, operations int, wall time.Duration, durations []time.Duration) {
	t.Helper()
	if len(durations) == 0 {
		t.Fatalf("%s recorded no durations", name)
	}
	p50 := percentile(durations, 0.50)
	p95 := percentile(durations, 0.95)
	p99 := percentile(durations, 0.99)
	p95Budget := loadBudgetP95(name)
	t.Logf("%s operations=%d wall=%s throughput=%.2f/s p50=%s p95=%s p99=%s",
		name,
		operations,
		wall,
		float64(operations)/wall.Seconds(),
		p50,
		p95,
		p99,
	)
	if p95 > p95Budget {
		t.Fatalf("%s p95=%s exceeds budget %s; override with GITSLICE_LOAD_BUDGET_P95_MS or %s", name, p95, p95Budget, loadBudgetEnvKey(name))
	}
}

func reportDurationsNoBudget(t *testing.T, name string, operations int, wall time.Duration, durations []time.Duration, objectStoreLatencyMS int) {
	t.Helper()
	if len(durations) == 0 {
		t.Fatalf("%s recorded no durations", name)
	}
	t.Logf("%s operations=%d wall=%s throughput=%.2f/s p50=%s p95=%s p99=%s object_store_latency_ms=%d",
		name,
		operations,
		wall,
		float64(operations)/wall.Seconds(),
		percentile(durations, 0.50),
		percentile(durations, 0.95),
		percentile(durations, 0.99),
		objectStoreLatencyMS,
	)
}

func percentile(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	idx := int(float64(len(durations)-1) * p)
	return durations[idx]
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func loadBudgetP95(name string) time.Duration {
	if value, ok := envMillis(loadBudgetEnvKey(name)); ok {
		return value
	}
	if value, ok := envMillis("GITSLICE_LOAD_BUDGET_P95_MS"); ok {
		return value
	}
	if value, ok := defaultScenarioBudgetP95[name]; ok {
		return value
	}
	return time.Duration(defaultLoadBudgetP95MS) * time.Millisecond
}

func loadBudgetEnvKey(name string) string {
	var b strings.Builder
	b.WriteString("GITSLICE_LOAD_BUDGET_")
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(unicode.ToUpper(r))
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteString("_P95_MS")
	return b.String()
}

func envMillis(key string) (time.Duration, bool) {
	raw := os.Getenv(key)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	return time.Duration(value) * time.Millisecond, true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
