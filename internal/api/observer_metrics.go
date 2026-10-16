package api

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	observerLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gerry_observer_last_success_timestamp_seconds", Help: "Unix timestamp of the last complete successful Kubernetes observation.",
	})
	observerErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gerry_observer_errors_total", Help: "Failed Kubernetes observation cycles; missing-host grace resets on failure.",
	})
	observedMissing = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gerry_observed_hosts_missing", Help: "Observer-owned hosts absent from the last complete snapshot and awaiting retirement.",
	}, []string{"zone"})
	observedRetired = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gerry_observed_hosts_retired_total", Help: "Missing observed hosts retired with an audit event.",
	}, []string{"zone"})
)

func RecordObserverSync(success bool) {
	if success {
		observerLastSuccess.SetToCurrentTime()
	} else {
		observerErrors.Inc()
	}
}

func SetObservationMetrics(zone string, missing, retired int) {
	observedMissing.WithLabelValues(zone).Set(float64(missing))
	observedRetired.WithLabelValues(zone).Add(float64(retired))
}
