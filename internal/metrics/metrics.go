package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Labels map[string]string

var defaultRegistry = NewRegistry()

type Registry struct {
	mu      sync.RWMutex
	metrics map[string]metric
}

type metric interface {
	name() string
	writePrometheus(io.Writer)
}

func NewRegistry() *Registry {
	return &Registry{metrics: map[string]metric{}}
}

func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		defaultRegistry.WritePrometheus(w)
	})
}

func WritePrometheus(w io.Writer) {
	defaultRegistry.WritePrometheus(w)
}

func (r *Registry) register(m metric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := m.name()
	if _, ok := r.metrics[name]; ok {
		panic("metrics: duplicate metric " + name)
	}
	r.metrics[name] = m
}

func (r *Registry) WritePrometheus(w io.Writer) {
	r.mu.RLock()
	names := make([]string, 0, len(r.metrics))
	for name := range r.metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	metrics := make([]metric, 0, len(names))
	for _, name := range names {
		metrics = append(metrics, r.metrics[name])
	}
	r.mu.RUnlock()
	for _, metric := range metrics {
		metric.writePrometheus(w)
	}
}

type Counter struct {
	mu         sync.Mutex
	metricName string
	help       string
	labelNames []string
	samples    map[string]*numberSample
}

type Gauge struct {
	mu         sync.Mutex
	metricName string
	help       string
	labelNames []string
	samples    map[string]*numberSample
}

type Histogram struct {
	mu         sync.Mutex
	metricName string
	help       string
	labelNames []string
	buckets    []float64
	samples    map[string]*histogramSample
}

type numberSample struct {
	labels []string
	value  float64
}

type histogramSample struct {
	labels []string
	bucket []uint64
	count  uint64
	sum    float64
}

func NewCounter(name, help string, labelNames ...string) *Counter {
	c := &Counter{
		metricName: name,
		help:       help,
		labelNames: append([]string(nil), labelNames...),
		samples:    map[string]*numberSample{},
	}
	defaultRegistry.register(c)
	return c
}

func NewGauge(name, help string, labelNames ...string) *Gauge {
	g := &Gauge{
		metricName: name,
		help:       help,
		labelNames: append([]string(nil), labelNames...),
		samples:    map[string]*numberSample{},
	}
	defaultRegistry.register(g)
	return g
}

func NewHistogram(name, help string, buckets []float64, labelNames ...string) *Histogram {
	copied := append([]float64(nil), buckets...)
	sort.Float64s(copied)
	unique := copied[:0]
	for _, bucket := range copied {
		if len(unique) == 0 || bucket != unique[len(unique)-1] {
			unique = append(unique, bucket)
		}
	}
	h := &Histogram{
		metricName: name,
		help:       help,
		labelNames: append([]string(nil), labelNames...),
		buckets:    append([]float64(nil), unique...),
		samples:    map[string]*histogramSample{},
	}
	defaultRegistry.register(h)
	return h
}

func (c *Counter) name() string {
	return c.metricName
}

func (c *Counter) Add(delta float64, labels Labels) {
	if delta < 0 {
		return
	}
	values := labelValues(c.labelNames, labels)
	key := labelKey(values)
	c.mu.Lock()
	sample := c.samples[key]
	if sample == nil {
		sample = &numberSample{labels: values}
		c.samples[key] = sample
	}
	sample.value += delta
	c.mu.Unlock()
}

func (c *Counter) Inc(labels Labels) {
	c.Add(1, labels)
}

func (c *Counter) writePrometheus(w io.Writer) {
	c.mu.Lock()
	samples := copyNumberSamples(c.samples)
	c.mu.Unlock()
	writeHeader(w, c.metricName, c.help, "counter")
	for _, sample := range samples {
		writeSample(w, c.metricName, c.labelNames, sample.labels, sample.value)
	}
}

func (g *Gauge) name() string {
	return g.metricName
}

func (g *Gauge) Set(value float64, labels Labels) {
	values := labelValues(g.labelNames, labels)
	key := labelKey(values)
	g.mu.Lock()
	sample := g.samples[key]
	if sample == nil {
		sample = &numberSample{labels: values}
		g.samples[key] = sample
	}
	sample.value = value
	g.mu.Unlock()
}

func (g *Gauge) writePrometheus(w io.Writer) {
	g.mu.Lock()
	samples := copyNumberSamples(g.samples)
	g.mu.Unlock()
	writeHeader(w, g.metricName, g.help, "gauge")
	for _, sample := range samples {
		writeSample(w, g.metricName, g.labelNames, sample.labels, sample.value)
	}
}

func (h *Histogram) name() string {
	return h.metricName
}

func (h *Histogram) Observe(value float64, labels Labels) {
	values := labelValues(h.labelNames, labels)
	key := labelKey(values)
	h.mu.Lock()
	sample := h.samples[key]
	if sample == nil {
		sample = &histogramSample{labels: values, bucket: make([]uint64, len(h.buckets))}
		h.samples[key] = sample
	}
	for i, bucket := range h.buckets {
		if value <= bucket {
			sample.bucket[i]++
		}
	}
	sample.count++
	sample.sum += value
	h.mu.Unlock()
}

func (h *Histogram) writePrometheus(w io.Writer) {
	h.mu.Lock()
	samples := make([]histogramSample, 0, len(h.samples))
	for _, sample := range h.samples {
		copied := histogramSample{
			labels: append([]string(nil), sample.labels...),
			bucket: append([]uint64(nil), sample.bucket...),
			count:  sample.count,
			sum:    sample.sum,
		}
		samples = append(samples, copied)
	}
	h.mu.Unlock()
	sort.Slice(samples, func(i, j int) bool {
		return labelKey(samples[i].labels) < labelKey(samples[j].labels)
	})
	writeHeader(w, h.metricName, h.help, "histogram")
	bucketLabelNames := append(append([]string(nil), h.labelNames...), "le")
	for _, sample := range samples {
		for i, bucket := range h.buckets {
			writeSample(w, h.metricName+"_bucket", bucketLabelNames, append(sample.labels, formatFloat(bucket)), float64(sample.bucket[i]))
		}
		writeSample(w, h.metricName+"_bucket", bucketLabelNames, append(sample.labels, "+Inf"), float64(sample.count))
		writeSample(w, h.metricName+"_sum", h.labelNames, sample.labels, sample.sum)
		writeSample(w, h.metricName+"_count", h.labelNames, sample.labels, float64(sample.count))
	}
}

func copyNumberSamples(samples map[string]*numberSample) []numberSample {
	out := make([]numberSample, 0, len(samples))
	for _, sample := range samples {
		out = append(out, numberSample{labels: append([]string(nil), sample.labels...), value: sample.value})
	}
	sort.Slice(out, func(i, j int) bool {
		return labelKey(out[i].labels) < labelKey(out[j].labels)
	})
	return out
}

func labelValues(names []string, labels Labels) []string {
	values := make([]string, len(names))
	for i, name := range names {
		values[i] = labels[name]
	}
	return values
}

func labelKey(values []string) string {
	var b strings.Builder
	for _, value := range values {
		b.WriteString(strconv.Itoa(len(value)))
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte(';')
	}
	return b.String()
}

func writeHeader(w io.Writer, name, help, metricType string) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, escapeHelp(help))
	fmt.Fprintf(w, "# TYPE %s %s\n", name, metricType)
}

func writeSample(w io.Writer, name string, labelNames, values []string, value float64) {
	fmt.Fprint(w, name)
	if len(labelNames) > 0 {
		fmt.Fprint(w, "{")
		for i, label := range labelNames {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `%s="%s"`, label, escapeLabel(values[i]))
		}
		fmt.Fprint(w, "}")
	}
	fmt.Fprintf(w, " %s\n", formatFloat(value))
}

func escapeHelp(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
