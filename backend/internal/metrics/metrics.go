package metrics

import (
	"bkt/internal/database"
	"bkt/internal/models"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bkt_http_requests_total",
		Help: "Total number of HTTP requests",
	}, []string{"method", "path", "status"})

	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "bkt_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	StorageBucketsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bkt_storage_buckets_total",
		Help: "Total number of storage buckets",
	})

	StorageObjectsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bkt_storage_objects_total",
		Help: "Total number of stored objects",
	})

	StorageBytesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bkt_storage_bytes_total",
		Help: "Total bytes of stored objects",
	})

	AuthFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "bkt_auth_failures_total",
		Help: "Total number of authentication failures",
	}, []string{"reason"})

	ActiveUsersTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bkt_active_users_total",
		Help: "Total number of non-locked user accounts",
	})
)

// StartStorageMetricsCollector periodically updates storage-level gauges from the database
func StartStorageMetricsCollector() {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		collectStorageMetrics()
		for range ticker.C {
			collectStorageMetrics()
		}
	}()
}

func collectStorageMetrics() {
	var bucketCount int64
	database.DB.Model(&models.Bucket{}).Count(&bucketCount)
	StorageBucketsTotal.Set(float64(bucketCount))

	var objectCount int64
	database.DB.Model(&models.Object{}).Count(&objectCount)
	StorageObjectsTotal.Set(float64(objectCount))

	var totalBytes struct{ Sum int64 }
	database.DB.Model(&models.Object{}).Select("COALESCE(SUM(size), 0) as sum").Scan(&totalBytes)
	StorageBytesTotal.Set(float64(totalBytes.Sum))

	var userCount int64
	database.DB.Model(&models.User{}).Where("is_locked = false").Count(&userCount)
	ActiveUsersTotal.Set(float64(userCount))
}
