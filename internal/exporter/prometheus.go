// Package exporter provides Prometheus metrics, REST API, and Slack alerts.
package exporter

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Mihir99-mk/koprobe/internal/aggregator"
)

// PrometheusExporter exposes cost metrics at /metrics.
type PrometheusExporter struct {
	agg  *aggregator.Aggregator
	port int
}

func NewPrometheus(agg *aggregator.Aggregator, port int) *PrometheusExporter {
	return &PrometheusExporter{agg: agg, port: port}
}

func (p *PrometheusExporter) Start(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", p.handleMetrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	server := &http.Server{Addr: fmt.Sprintf(":%d", p.port), Handler: mux}

	go func() {
		<-ctx.Done()
		server.Close()
	}()

	server.ListenAndServe()
}

func (p *PrometheusExporter) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap := p.agg.LatestSnapshot()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	// Pod-level cost metrics
	for _, cost := range snap.PodCosts {
		labels := fmt.Sprintf(
			`namespace="%s",pod="%s",team="%s",service="%s",env="%s"`,
			cost.Namespace, cost.PodName, cost.Team, cost.Service, cost.Environment,
		)

		fmt.Fprintf(w, "kubefinbpf_pod_cost_total{%s} %f\n", labels, cost.TotalCost)
		fmt.Fprintf(w, "kubefinbpf_pod_cpu_cost{%s} %f\n", labels, cost.CPUCost)
		fmt.Fprintf(w, "kubefinbpf_pod_memory_cost{%s} %f\n", labels, cost.MemoryCost)
		fmt.Fprintf(w, "kubefinbpf_pod_network_cost{%s} %f\n", labels, cost.NetworkCost)
		fmt.Fprintf(w, "kubefinbpf_pod_disk_cost{%s} %f\n", labels, cost.DiskCost)
		fmt.Fprintf(w, "kubefinbpf_pod_wasted_dollars{%s} %f\n", labels, cost.WastedDollars)
		fmt.Fprintf(w, "kubefinbpf_pod_cpu_utilization_pct{%s} %f\n", labels, cost.CPUUtilizationPct)

		// Raw usage metrics
		fmt.Fprintf(w, "kubefinbpf_pod_network_egress_bytes{%s} %f\n", labels, cost.NetworkEgressGB*float64(1<<30))
		fmt.Fprintf(w, "kubefinbpf_pod_network_internet_bytes{%s} %f\n", labels, cost.NetworkInternetGB*float64(1<<30))
	}

	// Team-level aggregated metrics
	for team, tc := range snap.TeamCosts {
		labels := fmt.Sprintf(`team="%s"`, team)
		fmt.Fprintf(w, "kubefinbpf_team_cost_total{%s} %f\n", labels, tc.TotalCost)
		fmt.Fprintf(w, "kubefinbpf_team_wasted_dollars{%s} %f\n", labels, tc.WastedCost)
		fmt.Fprintf(w, "kubefinbpf_team_pod_count{%s} %d\n", labels, tc.PodCount)
	}

	// Cluster total
	fmt.Fprintf(w, "kubefinbpf_cluster_cost_total %f\n", snap.Total)
}
