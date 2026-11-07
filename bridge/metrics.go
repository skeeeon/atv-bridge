package bridge

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics never carry a per-TV label, so cardinality stays fixed.
type Metrics struct {
	devices     *prometheus.GaugeVec
	commands    *prometheus.CounterVec
	stateWrites *prometheus.CounterVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	return &Metrics{
		devices: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "atv_devices", Help: "TVs by status: connected, unreachable, unpaired, invalid.",
		}, []string{"status"}),
		commands: f.NewCounterVec(prometheus.CounterOpts{
			Name: "atv_commands_total", Help: "Requests by endpoint and result (ok, or the error code).",
		}, []string{"verb", "result"}),
		stateWrites: f.NewCounterVec(prometheus.CounterOpts{
			Name: "atv_state_writes_total", Help: "Writes to the state bucket by result (ok, error).",
		}, []string{"result"}),
	}
}
