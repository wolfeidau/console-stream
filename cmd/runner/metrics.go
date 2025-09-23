package main

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// LocalStorage stores metrics in memory
type LocalStorage struct {
	mu      sync.RWMutex
	metrics map[string][]metricdata.Metrics
}

func NewLocalStorage() *LocalStorage {
	return &LocalStorage{
		metrics: make(map[string][]metricdata.Metrics),
	}
}

func (ls *LocalStorage) Store(scopeName string, metrics []metricdata.Metrics) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.metrics[scopeName] = metrics
}

func (ls *LocalStorage) GetHistograms() map[string][]HistogramSnapshot {
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	result := make(map[string][]HistogramSnapshot)

	for scope, metrics := range ls.metrics {
		var histograms []HistogramSnapshot
		for _, m := range metrics {
			// Handle float64 histograms
			if hist, ok := m.Data.(metricdata.Histogram[float64]); ok {
				for _, dp := range hist.DataPoints {
					hs := HistogramSnapshot{
						Name:         m.Name,
						Description:  m.Description,
						Count:        dp.Count,
						Sum:          dp.Sum,
						Bounds:       dp.Bounds,
						BucketCounts: dp.BucketCounts,
						Timestamp:    dp.Time,
					}

					if min, ok := dp.Min.Value(); ok {
						hs.Min = min
					}
					if max, ok := dp.Max.Value(); ok {
						hs.Max = max
					}

					histograms = append(histograms, hs)
				}
			}

			// Handle int64 histograms
			if hist, ok := m.Data.(metricdata.Histogram[int64]); ok {
				for _, dp := range hist.DataPoints {
					hs := HistogramSnapshot{
						Name:         m.Name,
						Description:  m.Description,
						Count:        dp.Count,
						Sum:          float64(dp.Sum), // Convert int64 to float64
						Bounds:       dp.Bounds,
						BucketCounts: dp.BucketCounts,
						Timestamp:    dp.Time,
					}

					if min, ok := dp.Min.Value(); ok {
						hs.Min = float64(min)
					}
					if max, ok := dp.Max.Value(); ok {
						hs.Max = float64(max)
					}

					histograms = append(histograms, hs)
				}
			}
		}
		result[scope] = histograms
	}

	return result
}

type HistogramSnapshot struct {
	Name         string
	Description  string
	Count        uint64
	Sum          float64
	Min          float64
	Max          float64
	Bounds       []float64
	BucketCounts []uint64
	Timestamp    time.Time
}

// Custom exporter
type LocalExporter struct {
	storage *LocalStorage
}

func NewLocalExporter(storage *LocalStorage) *LocalExporter {
	return &LocalExporter{storage: storage}
}

func (e *LocalExporter) Temporality(metric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (e *LocalExporter) Aggregation(metric.InstrumentKind) metric.Aggregation {
	// Define explicit bucket boundaries for better histogram visualization
	// These buckets cover byte ranges from small (1 byte) to large (10MB+)
	buckets := []float64{
		0x10, 0x20, 0x40, 0x80,
		0x100, 0x200, 0x400, 0x800,
		0x1000, 0x2000, 0x4000, 0x8000,
		0x10000, 0x20000, 0x40000, 0x80000,
		0x100000, 0x200000, 0x400000, 0x800000,
	}
	return metric.AggregationExplicitBucketHistogram{
		Boundaries: buckets,
	}
}

func (e *LocalExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	for _, sm := range rm.ScopeMetrics {
		e.storage.Store(sm.Scope.Name, sm.Metrics)
	}
	return nil
}

func (e *LocalExporter) ForceFlush(ctx context.Context) error {
	return nil
}

func (e *LocalExporter) Shutdown(ctx context.Context) error {
	return nil
}
