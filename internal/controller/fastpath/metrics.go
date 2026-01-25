package fastpath

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	createSandboxDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "fastpath_create_sandbox_duration_seconds",
			Help:    "Duration of CreateSandbox RPC calls",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		},
		[]string{"mode", "success"},
	)
)
