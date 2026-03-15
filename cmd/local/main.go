// Koprobe — Local Simulator
// Runs a full cost attribution server with simulated pod data.
// No K8s, no eBPF, no root needed.
//
// Usage: go run ./cmd/local

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ────────────────────────────────────────────────────────────────
// Simulator
// ────────────────────────────────────────────────────────────────

type PodProfile struct {
	PodName          string
	Namespace        string
	Team             string
	Service          string
	Feature          string
	Environment      string
	CPUIntensity     float64
	NetworkIntensity float64
	DiskIntensity    float64
	MemIntensity     float64
	SpikeEvery       time.Duration
	SpikeMultiplier  float64
	lastSpike        time.Time
}

type PodMetrics struct {
	PodName           string  `json:"pod_name"`
	Namespace         string  `json:"namespace"`
	Team              string  `json:"team"`
	Service           string  `json:"service"`
	Feature           string  `json:"feature"`
	Environment       string  `json:"environment"`
	CPUCyclesBillion  float64 `json:"cpu_cycles_billion"`
	MemoryGBHours     float64 `json:"memory_gb_hours"`
	NetworkEgressGB   float64 `json:"network_egress_gb"`
	NetworkCrossAZGB  float64 `json:"network_cross_az_gb"`
	NetworkInternetGB float64 `json:"network_internet_gb"`
	DiskReadMIOPS     float64 `json:"disk_read_mIOPS"`
	DiskWriteMIOPS    float64 `json:"disk_write_mIOPS"`
	CPUUtilPct        float64 `json:"cpu_util_pct"`
	WastedDollars     float64 `json:"wasted_dollars"`
	CPUCost           float64 `json:"cpu_cost"`
	MemoryCost        float64 `json:"memory_cost"`
	NetworkCost       float64 `json:"network_cost"`
	DiskCost          float64 `json:"disk_cost"`
	TotalCost         float64 `json:"total_cost"`
}

type Simulator struct {
	pods []*PodProfile
	rng  *rand.Rand
	tick int
}

func NewSimulator() *Simulator {
	return &Simulator{
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
		pods: buildCluster(),
	}
}

func (s *Simulator) Tick() []*PodMetrics {
	s.tick++
	now := time.Now()
	var out []*PodMetrics
	for _, p := range s.pods {
		out = append(out, s.generate(p, now))
	}
	return out
}

func (s *Simulator) generate(p *PodProfile, now time.Time) *PodMetrics {
	mult := 1.0
	if p.SpikeEvery > 0 && now.Sub(p.lastSpike) > p.SpikeEvery && s.rng.Float64() < 0.12 {
		p.lastSpike = now
		mult = p.SpikeMultiplier
	}

	wave := 0.25 * math.Sin(float64(s.tick)/18.0)
	noise := func() float64 { return (s.rng.Float64() - 0.5) * 0.15 }

	cpu := clamp(p.CPUIntensity+wave+noise()) * mult
	net := clamp(p.NetworkIntensity+noise()) * mult
	disk := clamp(p.DiskIntensity+noise()) * mult
	mem := clamp(p.MemIntensity + noise()*0.08)

	const window = 15.0 / 3600.0 // 15-second window in hours

	cpuB := cpu * 8.0
	memGB := mem * 4.0
	netE := net * 0.05
	netI := netE * 0.3
	netAZ := netE * 0.2
	diskR := disk * 2.0
	diskW := disk * 1.5

	cpuCost := cpuB * 0.000048 * window
	memCost := memGB * 0.0125 * window
	netCost := netI*0.09 + netAZ*0.01
	diskCost := diskR*0.005 + diskW*0.010
	total := cpuCost + memCost + netCost + diskCost

	requested := p.CPUIntensity * 8.0 * 1.5
	utilPct := 0.0
	if requested > 0 {
		utilPct = (cpuB / requested) * 100
	}
	wasted := 0.0
	if utilPct < 20 {
		wasted = cpuCost * (1.0 - utilPct/100.0)
	}

	return &PodMetrics{
		PodName: p.PodName, Namespace: p.Namespace,
		Team: p.Team, Service: p.Service,
		Feature: p.Feature, Environment: p.Environment,
		CPUCyclesBillion:  round(cpuB, 3),
		MemoryGBHours:     round(memGB*window, 5),
		NetworkEgressGB:   round(netE, 5),
		NetworkCrossAZGB:  round(netAZ, 5),
		NetworkInternetGB: round(netI, 5),
		DiskReadMIOPS:     round(diskR, 3),
		DiskWriteMIOPS:    round(diskW, 3),
		CPUUtilPct:        round(utilPct, 1),
		WastedDollars:     round(wasted, 6),
		CPUCost:           round(cpuCost, 6),
		MemoryCost:        round(memCost, 6),
		NetworkCost:       round(netCost, 6),
		DiskCost:          round(diskCost, 6),
		TotalCost:         round(total, 6),
	}
}

// ────────────────────────────────────────────────────────────────
// Store
// ────────────────────────────────────────────────────────────────

type HistoryPoint struct {
	Time  time.Time          `json:"time"`
	Total float64            `json:"total"`
	Teams map[string]float64 `json:"teams"`
}

type Alert struct {
	Time    time.Time `json:"time"`
	Team    string    `json:"team"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type Store struct {
	mu        sync.RWMutex
	latest    []*PodMetrics
	history   []HistoryPoint
	baselines map[string]float64
	alerts    []Alert
	start     time.Time
}

func NewStore() *Store {
	return &Store{baselines: make(map[string]float64), start: time.Now()}
}

func (st *Store) Update(metrics []*PodMetrics) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.latest = metrics
	teamTotals := make(map[string]float64)
	total := 0.0
	for _, m := range metrics {
		teamTotals[m.Team] += m.TotalCost
		total += m.TotalCost
	}

	st.history = append(st.history, HistoryPoint{Time: time.Now(), Total: total, Teams: teamTotals})
	if len(st.history) > 480 {
		st.history = st.history[1:]
	}

	for team, cost := range teamTotals {
		base, ok := st.baselines[team]
		if !ok {
			st.baselines[team] = cost
			continue
		}
		if base > 0 && cost > base*2.5 {
			pct := ((cost - base) / base) * 100
			st.alerts = append(st.alerts, Alert{
				Time: time.Now(), Team: team, Level: "critical",
				Message: fmt.Sprintf("🔴 Cost spike +%.0f%% above baseline (now $%.5f)", pct, cost),
			})
		} else if base > 0 && cost > base*1.5 {
			pct := ((cost - base) / base) * 100
			st.alerts = append(st.alerts, Alert{
				Time: time.Now(), Team: team, Level: "warning",
				Message: fmt.Sprintf("🟡 Cost trending up +%.0f%%", pct),
			})
		}
		st.baselines[team] = base*0.92 + cost*0.08
	}
	if len(st.alerts) > 50 {
		st.alerts = st.alerts[len(st.alerts)-50:]
	}
}

// ────────────────────────────────────────────────────────────────
// HTTP Server
// ────────────────────────────────────────────────────────────────

type Server struct {
	store *Store
	port  int
}

func NewServer(st *Store, port int) *Server { return &Server{store: st, port: port} }

func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/costs/summary", s.summary)
	mux.HandleFunc("/api/v1/costs/by-team", s.byTeam)
	mux.HandleFunc("/api/v1/costs/by-pod", s.byPod)
	mux.HandleFunc("/api/v1/costs/waste", s.waste)
	mux.HandleFunc("/api/v1/costs/history", s.history)
	mux.HandleFunc("/api/v1/alerts", s.alertsHandler)
	mux.HandleFunc("/metrics", s.prometheus)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })

	http.ListenAndServe(fmt.Sprintf(":%d", s.port), cors(mux))
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	total, wasted := 0.0, 0.0
	teams := make(map[string]float64)
	for _, m := range s.store.latest {
		total += m.TotalCost
		wasted += m.WastedDollars
		teams[m.Team] += m.TotalCost
	}
	writeJSON(w, map[string]interface{}{
		"timestamp": time.Now(), "total_cost": total, "wasted_cost": wasted,
		"waste_pct": pct(wasted, total), "pod_count": len(s.store.latest),
		"team_count": len(teams), "uptime_seconds": time.Since(s.store.start).Seconds(),
	})
}

type TeamRow struct {
	Team        string  `json:"team"`
	TotalCost   float64 `json:"total_cost"`
	CPUCost     float64 `json:"cpu_cost"`
	MemoryCost  float64 `json:"memory_cost"`
	NetworkCost float64 `json:"network_cost"`
	DiskCost    float64 `json:"disk_cost"`
	WastedCost  float64 `json:"wasted_cost"`
	WastePct    float64 `json:"waste_pct"`
	PodCount    int     `json:"pod_count"`
}

func (s *Server) byTeam(w http.ResponseWriter, r *http.Request) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	tm := make(map[string]*TeamRow)
	for _, m := range s.store.latest {
		if _, ok := tm[m.Team]; !ok {
			tm[m.Team] = &TeamRow{Team: m.Team}
		}
		e := tm[m.Team]
		e.TotalCost += m.TotalCost
		e.CPUCost += m.CPUCost
		e.MemoryCost += m.MemoryCost
		e.NetworkCost += m.NetworkCost
		e.DiskCost += m.DiskCost
		e.WastedCost += m.WastedDollars
		e.PodCount++
	}
	var rows []*TeamRow
	for _, r2 := range tm {
		r2.WastePct = pct(r2.WastedCost, r2.TotalCost)
		rows = append(rows, r2)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].TotalCost > rows[j].TotalCost })
	writeJSON(w, map[string]interface{}{"teams": rows, "timestamp": time.Now()})
}

func (s *Server) byPod(w http.ResponseWriter, r *http.Request) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	team, ns := r.URL.Query().Get("team"), r.URL.Query().Get("namespace")
	var pods []*PodMetrics
	for _, m := range s.store.latest {
		if team != "" && m.Team != team {
			continue
		}
		if ns != "" && m.Namespace != ns {
			continue
		}
		pods = append(pods, m)
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].TotalCost > pods[j].TotalCost })
	writeJSON(w, map[string]interface{}{"pods": pods, "timestamp": time.Now()})
}

func (s *Server) waste(w http.ResponseWriter, r *http.Request) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	var out []*PodMetrics
	tw := 0.0
	for _, m := range s.store.latest {
		if m.CPUUtilPct < 20 {
			out = append(out, m)
			tw += m.WastedDollars
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WastedDollars > out[j].WastedDollars })
	writeJSON(w, map[string]interface{}{"wasted_pods": out, "total_wasted": tw, "count": len(out)})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	writeJSON(w, map[string]interface{}{"history": s.store.history})
}

func (s *Server) alertsHandler(w http.ResponseWriter, r *http.Request) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	alerts := s.store.alerts
	if alerts == nil {
		alerts = []Alert{}
	}
	rev := make([]Alert, len(alerts))
	for i, a := range alerts {
		rev[len(alerts)-1-i] = a
	}
	writeJSON(w, map[string]interface{}{"alerts": rev, "count": len(rev)})
}

func (s *Server) prometheus(w http.ResponseWriter, r *http.Request) {
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	total := 0.0
	for _, m := range s.store.latest {
		lbl := fmt.Sprintf(`namespace="%s",pod="%s",team="%s",service="%s"`,
			m.Namespace, m.PodName, m.Team, m.Service)
		fmt.Fprintf(w, "kubefinbpf_pod_cost_total{%s} %f\n", lbl, m.TotalCost)
		fmt.Fprintf(w, "kubefinbpf_pod_cpu_cost{%s} %f\n", lbl, m.CPUCost)
		fmt.Fprintf(w, "kubefinbpf_pod_memory_cost{%s} %f\n", lbl, m.MemoryCost)
		fmt.Fprintf(w, "kubefinbpf_pod_network_cost{%s} %f\n", lbl, m.NetworkCost)
		fmt.Fprintf(w, "kubefinbpf_pod_disk_cost{%s} %f\n", lbl, m.DiskCost)
		fmt.Fprintf(w, "kubefinbpf_pod_wasted_dollars{%s} %f\n", lbl, m.WastedDollars)
		fmt.Fprintf(w, "kubefinbpf_pod_cpu_util_pct{%s} %f\n", lbl, m.CPUUtilPct)
		total += m.TotalCost
	}
	fmt.Fprintf(w, "kubefinbpf_cluster_cost_total %f\n", total)
}

// ────────────────────────────────────────────────────────────────
// Terminal Dashboard
// ────────────────────────────────────────────────────────────────

func printDashboard(store *Store, tick int) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	// Clear screen
	fmt.Print("\033[H\033[2J")

	fmt.Println("╔══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║          Koprobe  💰  Local Simulator                        ║")
	fmt.Printf("║  Tick %-4d │ %s                          ║\n", tick, time.Now().Format("15:04:05"))
	fmt.Println("╚══════════════════════════════════════════════════════════════════╝")

	// Cluster total
	total, wasted := 0.0, 0.0
	teamTotals := make(map[string]float64)
	for _, m := range store.latest {
		total += m.TotalCost
		wasted += m.WastedDollars
		teamTotals[m.Team] += m.TotalCost
	}
	fmt.Printf("\n  Cluster Cost (15s window): $%-10.5f  │  Wasted: $%.5f (%.1f%%)\n\n",
		total, wasted, pct(wasted, total))

	// Team breakdown
	fmt.Println("  TEAMS                Cost       CPU        Net        Waste")
	fmt.Println("  ─────────────────────────────────────────────────────────────")
	var teams []string
	for t := range teamTotals {
		teams = append(teams, t)
	}
	sort.Slice(teams, func(i, j int) bool { return teamTotals[teams[i]] > teamTotals[teams[j]] })

	for _, team := range teams {
		cost := teamTotals[team]
		barLen := int(cost / total * 20)
		bar := strings.Repeat("█", barLen) + strings.Repeat("░", 20-barLen)
		cpuC, netC, wasteC := 0.0, 0.0, 0.0
		for _, m := range store.latest {
			if m.Team == team {
				cpuC += m.CPUCost
				netC += m.NetworkCost
				wasteC += m.WastedDollars
			}
		}
		fmt.Printf("  %-12s %s $%-8.5f $%-8.6f $%-8.6f $%-8.6f\n",
			team, bar, cost, cpuC, netC, wasteC)
	}

	// Top 5 pods
	pods := make([]*PodMetrics, len(store.latest))
	copy(pods, store.latest)
	sort.Slice(pods, func(i, j int) bool { return pods[i].TotalCost > pods[j].TotalCost })

	fmt.Println("\n  TOP PODS             Team          Cost        CPU%")
	fmt.Println("  ─────────────────────────────────────────────────────────────")
	for i, p := range pods {
		if i >= 6 {
			break
		}
		util := fmt.Sprintf("%.0f%%", p.CPUUtilPct)
		flag := "  "
		if p.CPUUtilPct < 20 {
			flag = "💸"
		}
		if p.TotalCost > total*0.25 {
			flag = "🔥"
		}
		fmt.Printf("  %s %-22s %-12s $%-10.5f %s\n",
			flag, truncate(p.PodName, 22), p.Team, p.TotalCost, util)
	}

	// Recent alerts
	if len(store.alerts) > 0 {
		fmt.Println("\n  RECENT ALERTS")
		fmt.Println("  ─────────────────────────────────────────────────────────────")
		start := len(store.alerts) - 3
		if start < 0 {
			start = 0
		}
		for _, a := range store.alerts[start:] {
			fmt.Printf("  [%s] %s\n", a.Time.Format("15:04:05"), a.Message)
		}
	}

	fmt.Println("\n  ─────────────────────────────────────────────────────────────")
	fmt.Println("  API: http://localhost:8080/api/v1/costs/summary")
	fmt.Println("  Press Ctrl+C to stop")
}

// ────────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────────

func main() {
	port := flag.Int("port", 8080, "API server port")
	interval := flag.Duration("interval", 3*time.Second, "Simulation tick interval")
	noUI := flag.Bool("no-ui", false, "Disable terminal dashboard (API only)")
	flag.Parse()

	printBanner()

	sim := NewSimulator()
	store := NewStore()
	server := NewServer(store, *port)

	// Start API server
	go server.Start()

	fmt.Printf("\n  🚀 Running!  Tick every %s\n\n", *interval)
	fmt.Println("  Endpoints:")
	fmt.Printf("  → http://localhost:%d/api/v1/costs/summary\n", *port)
	fmt.Printf("  → http://localhost:%d/api/v1/costs/by-team\n", *port)
	fmt.Printf("  → http://localhost:%d/api/v1/costs/by-pod\n", *port)
	fmt.Printf("  → http://localhost:%d/api/v1/costs/waste\n", *port)
	fmt.Printf("  → http://localhost:%d/api/v1/costs/history\n", *port)
	fmt.Printf("  → http://localhost:%d/api/v1/alerts\n", *port)
	fmt.Printf("  → http://localhost:%d/metrics  (Prometheus)\n\n", *port)

	if !*noUI {
		time.Sleep(1 * time.Second) // let server start
	}

	tick := 0
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	// Initial tick
	metrics := sim.Tick()
	store.Update(metrics)

	for range ticker.C {
		tick++
		metrics = sim.Tick()
		store.Update(metrics)
		if !*noUI {
			printDashboard(store, tick)
		} else {
			total := 0.0
			for _, m := range metrics {
				total += m.TotalCost
			}
			fmt.Printf("[tick %d] cluster_cost=$%.5f pods=%d\n", tick, total, len(metrics))
		}
	}
}

func printBanner() {
	fmt.Println(`
 ██╗  ██╗██╗   ██╗██████╗ ███████╗    ███████╗██╗███╗  ██╗
 ██║ ██╔╝██║   ██║██╔══██╗██╔════╝    ██╔════╝██║████╗ ██║
 █████╔╝ ██║   ██║██████╔╝█████╗      █████╗  ██║██╔██╗██║
 ██╔═██╗ ██║   ██║██╔══██╗██╔══╝      ██╔══╝  ██║██║╚████║
 ██║  ██╗╚██████╔╝██████╔╝███████╗    ██║     ██║██║ ╚███║
 ╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚══════╝   ╚═╝     ╚═╝╚═╝  ╚══╝
         Local Simulator — No K8s or eBPF needed`)
}

// ────────────────────────────────────────────────────────────────
// Cluster definition
// ────────────────────────────────────────────────────────────────

func buildCluster() []*PodProfile {
	return []*PodProfile{
		{PodName: "payment-api-7d9f2", Namespace: "production", Team: "backend",
			Service: "payment-api", Feature: "checkout", Environment: "production",
			CPUIntensity: 0.75, NetworkIntensity: 0.80, DiskIntensity: 0.30, MemIntensity: 0.60,
			SpikeEvery: 90 * time.Second, SpikeMultiplier: 3.2},
		{PodName: "auth-service-abc12", Namespace: "production", Team: "backend",
			Service: "auth-service", Feature: "login", Environment: "production",
			CPUIntensity: 0.45, NetworkIntensity: 0.50, DiskIntensity: 0.10, MemIntensity: 0.40,
			SpikeEvery: 120 * time.Second, SpikeMultiplier: 2.1},
		{PodName: "user-service-def34", Namespace: "production", Team: "backend",
			Service: "user-service", Feature: "profile", Environment: "production",
			CPUIntensity: 0.30, NetworkIntensity: 0.35, DiskIntensity: 0.20, MemIntensity: 0.35},
		{PodName: "order-worker-ghi56", Namespace: "production", Team: "backend",
			Service: "order-worker", Feature: "orders", Environment: "production",
			CPUIntensity: 0.60, NetworkIntensity: 0.25, DiskIntensity: 0.55, MemIntensity: 0.50,
			SpikeEvery: 60 * time.Second, SpikeMultiplier: 2.5},
		{PodName: "model-training-jkl78", Namespace: "ml", Team: "ml",
			Service: "training-job", Feature: "recommendations", Environment: "production",
			CPUIntensity: 0.95, NetworkIntensity: 0.20, DiskIntensity: 0.80, MemIntensity: 0.90,
			SpikeEvery: 45 * time.Second, SpikeMultiplier: 1.5},
		{PodName: "feature-store-mno90", Namespace: "ml", Team: "ml",
			Service: "feature-store", Feature: "recommendations", Environment: "production",
			CPUIntensity: 0.55, NetworkIntensity: 0.60, DiskIntensity: 0.70, MemIntensity: 0.75},
		{PodName: "inference-pqr12", Namespace: "ml", Team: "ml",
			Service: "inference", Feature: "recommendations", Environment: "production",
			CPUIntensity: 0.70, NetworkIntensity: 0.40, DiskIntensity: 0.15, MemIntensity: 0.65,
			SpikeEvery: 75 * time.Second, SpikeMultiplier: 2.8},
		{PodName: "web-app-stu34", Namespace: "production", Team: "frontend",
			Service: "web-app", Feature: "homepage", Environment: "production",
			CPUIntensity: 0.25, NetworkIntensity: 0.70, DiskIntensity: 0.05, MemIntensity: 0.30},
		{PodName: "cdn-proxy-vwx56", Namespace: "production", Team: "frontend",
			Service: "cdn-proxy", Feature: "assets", Environment: "production",
			CPUIntensity: 0.15, NetworkIntensity: 0.90, DiskIntensity: 0.10, MemIntensity: 0.20,
			SpikeEvery: 100 * time.Second, SpikeMultiplier: 1.8},
		{PodName: "kafka-consumer-yza12", Namespace: "data", Team: "data",
			Service: "kafka-consumer", Feature: "analytics", Environment: "production",
			CPUIntensity: 0.40, NetworkIntensity: 0.55, DiskIntensity: 0.65, MemIntensity: 0.55},
		{PodName: "spark-driver-bcd34", Namespace: "data", Team: "data",
			Service: "spark", Feature: "etl", Environment: "production",
			CPUIntensity: 0.85, NetworkIntensity: 0.30, DiskIntensity: 0.90, MemIntensity: 0.80,
			SpikeEvery: 50 * time.Second, SpikeMultiplier: 1.6},
		{PodName: "legacy-worker-zzz99", Namespace: "production", Team: "backend",
			Service: "legacy-worker", Feature: "deprecated", Environment: "production",
			CPUIntensity: 0.04, NetworkIntensity: 0.02, DiskIntensity: 0.01, MemIntensity: 0.50},
		{PodName: "staging-api-xyz00", Namespace: "staging", Team: "frontend",
			Service: "web-app", Feature: "homepage", Environment: "staging",
			CPUIntensity: 0.03, NetworkIntensity: 0.05, DiskIntensity: 0.02, MemIntensity: 0.40},
	}
}

// ────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────

func clamp(v float64) float64 {
	if v < 0.05 {
		return 0.05
	}
	if v > 1.0 {
		return 1.0
	}
	return v
}
func round(v float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}
func pct(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return math.Round((part/total)*1000) / 10
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == "OPTIONS" {
			w.WriteHeader(200)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func init() {
	// Suppress unused import if os is not used elsewhere
}
