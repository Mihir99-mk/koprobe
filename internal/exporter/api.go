package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/Mihir99-mk/koprobe/internal/aggregator"
)

// APIServer provides a REST API for cost data.
type APIServer struct {
	agg  *aggregator.Aggregator
	port int
}

func NewAPIServer(agg *aggregator.Aggregator, port int) *APIServer {
	return &APIServer{agg: agg, port: port}
}

func (s *APIServer) Start(ctx context.Context) {
	mux := http.NewServeMux()

	// Cost endpoints
	mux.HandleFunc("/api/v1/costs/summary", s.handleSummary)
	mux.HandleFunc("/api/v1/costs/by-team", s.handleByTeam)
	mux.HandleFunc("/api/v1/costs/by-namespace", s.handleByNamespace)
	mux.HandleFunc("/api/v1/costs/by-pod", s.handleByPod)
	mux.HandleFunc("/api/v1/costs/waste", s.handleWaste)
	mux.HandleFunc("/api/v1/costs/history", s.handleHistory)

	// Add CORS middleware
	handler := corsMiddleware(mux)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: handler,
	}

	go func() {
		<-ctx.Done()
		server.Close()
	}()

	server.ListenAndServe()
}

func (s *APIServer) handleSummary(w http.ResponseWriter, r *http.Request) {
	snap := s.agg.LatestSnapshot()

	response := map[string]interface{}{
		"timestamp":  snap.Timestamp,
		"total_cost": snap.Total,
		"pod_count":  len(snap.PodCosts),
		"team_count": len(snap.TeamCosts),
		"period":     "15s",
	}
	writeJSON(w, response)
}

func (s *APIServer) handleByTeam(w http.ResponseWriter, r *http.Request) {
	snap := s.agg.LatestSnapshot()

	type teamEntry struct {
		Team       string  `json:"team"`
		TotalCost  float64 `json:"total_cost"`
		WastedCost float64 `json:"wasted_cost"`
		PodCount   int     `json:"pod_count"`
		CPUCost    float64 `json:"cpu_cost"`
		MemCost    float64 `json:"memory_cost"`
		NetCost    float64 `json:"network_cost"`
		DiskCost   float64 `json:"disk_cost"`
	}

	var teams []teamEntry
	for _, tc := range snap.TeamCosts {
		var cpu, mem, net, disk float64
		for _, p := range tc.Pods {
			cpu += p.CPUCost
			mem += p.MemoryCost
			net += p.NetworkCost
			disk += p.DiskCost
		}
		teams = append(teams, teamEntry{
			Team: tc.Team, TotalCost: tc.TotalCost,
			WastedCost: tc.WastedCost, PodCount: tc.PodCount,
			CPUCost: cpu, MemCost: mem, NetCost: net, DiskCost: disk,
		})
	}

	sort.Slice(teams, func(i, j int) bool {
		return teams[i].TotalCost > teams[j].TotalCost
	})

	writeJSON(w, map[string]interface{}{"teams": teams, "timestamp": snap.Timestamp})
}

func (s *APIServer) handleByNamespace(w http.ResponseWriter, r *http.Request) {
	snap := s.agg.LatestSnapshot()
	nsCosts := make(map[string]float64)
	for _, p := range snap.PodCosts {
		nsCosts[p.Namespace] += p.TotalCost
	}
	writeJSON(w, map[string]interface{}{"namespaces": nsCosts, "timestamp": snap.Timestamp})
}

func (s *APIServer) handleByPod(w http.ResponseWriter, r *http.Request) {
	snap := s.agg.LatestSnapshot()
	ns := r.URL.Query().Get("namespace")
	team := r.URL.Query().Get("team")

	var pods []*aggregator.PodCost
	for _, p := range snap.PodCosts {
		if ns != "" && p.Namespace != ns {
			continue
		}
		if team != "" && p.Team != team {
			continue
		}
		pods = append(pods, p)
	}

	sort.Slice(pods, func(i, j int) bool {
		return pods[i].TotalCost > pods[j].TotalCost
	})

	writeJSON(w, map[string]interface{}{"pods": pods, "timestamp": snap.Timestamp})
}

func (s *APIServer) handleWaste(w http.ResponseWriter, r *http.Request) {
	wasted := s.agg.WasteReport()
	var totalWasted float64
	for _, p := range wasted {
		totalWasted += p.WastedDollars
	}
	writeJSON(w, map[string]interface{}{
		"wasted_pods":          wasted,
		"total_wasted_dollars": totalWasted,
		"timestamp":            time.Now(),
	})
}

func (s *APIServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	duration := 24 * time.Hour
	if d := r.URL.Query().Get("hours"); d != "" {
		var hours float64
		fmt.Sscanf(d, "%f", &hours)
		duration = time.Duration(hours * float64(time.Hour))
	}

	history := s.agg.History(duration)
	type point struct {
		Time time.Time `json:"time"`
		Cost float64   `json:"cost"`
	}
	var points []point
	for _, snap := range history {
		points = append(points, point{Time: snap.Timestamp, Cost: snap.Total})
	}
	writeJSON(w, map[string]interface{}{"history": points})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
