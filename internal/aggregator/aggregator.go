// Package aggregator combines raw eBPF metrics with K8s metadata
// and cloud pricing to produce accurate per-pod cost records.
package aggregator

import (
	"context"
	"sync"
	"time"

	"github.com/Mihir99-mk/koprobe/internal/collector"
	"github.com/Mihir99-mk/koprobe/internal/enricher"
	"github.com/Mihir99-mk/koprobe/internal/pricing"
)

const GB = 1 << 30

// PodCost holds the complete cost attribution for a single pod
// over a measurement window.
type PodCost struct {
	// Identity
	PodName     string
	Namespace   string
	NodeName    string
	Team        string
	Service     string
	Feature     string
	CostCenter  string
	Environment string

	// Window
	PeriodStart time.Time
	PeriodEnd   time.Time

	// Raw usage (from eBPF)
	CPUCyclesBillion  float64
	MemoryGBHours     float64
	NetworkEgressGB   float64
	NetworkCrossAZGB  float64
	NetworkInternetGB float64
	DiskReadMIOPS     float64
	DiskWriteMIOPS    float64

	// K8s requested (for waste calculation)
	CPURequestedMillis int64
	MemoryRequestedMB  int64

	// Computed costs ($)
	CPUCost     float64
	MemoryCost  float64
	NetworkCost float64
	DiskCost    float64
	TotalCost   float64

	// Waste metrics
	CPUUtilizationPct    float64
	MemoryUtilizationPct float64
	WastedDollars        float64
}

// TeamCost is an aggregation of all pod costs for a team.
type TeamCost struct {
	Team       string
	TotalCost  float64
	PodCount   int
	WastedCost float64
	Pods       []*PodCost
}

// Aggregator continuously reads from eBPF collectors,
// enriches with K8s metadata, and computes costs.
type Aggregator struct {
	collectors *collector.Manager
	enricher   *enricher.Enricher
	pricer     *pricing.Pricer

	mu       sync.RWMutex
	podCosts map[string]*PodCost // key: namespace/podname
	history  []*Snapshot
}

// Snapshot is a point-in-time cost summary.
type Snapshot struct {
	Timestamp time.Time
	PodCosts  []*PodCost
	TeamCosts map[string]*TeamCost
	Total     float64
}

func New(c *collector.Manager, e *enricher.Enricher, p *pricing.Pricer) *Aggregator {
	return &Aggregator{
		collectors: c,
		enricher:   e,
		pricer:     p,
		podCosts:   make(map[string]*PodCost),
	}
}

// Run starts the aggregation loop. Runs until ctx is cancelled.
func (a *Aggregator) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.collect()
		}
	}
}

func (a *Aggregator) collect() {
	prices := a.pricer.Get()
	window := 15 * time.Second
	now := time.Now()
	windowHours := window.Hours()

	rawMetrics := a.collectors.Snapshot()
	newCosts := make(map[string]*PodCost)

	for cgroupID, metrics := range rawMetrics {
		pod := a.enricher.Resolve(cgroupID)
		if pod == nil {
			continue // not a K8s pod (system process etc.)
		}

		key := pod.Namespace + "/" + pod.PodName

		cost := &PodCost{
			PodName:     pod.PodName,
			Namespace:   pod.Namespace,
			NodeName:    pod.NodeName,
			Team:        pod.TeamLabel,
			Service:     pod.ServiceLabel,
			Feature:     pod.FeatureLabel,
			CostCenter:  pod.CostCenter,
			Environment: pod.Environment,
			PeriodStart: now.Add(-window),
			PeriodEnd:   now,
		}

		// Convert raw eBPF measurements to cost-model units
		cost.CPUCyclesBillion = float64(metrics.CPUCycles) / 1e9
		cost.MemoryGBHours = (float64(metrics.MemoryBytesAvg) / float64(GB)) * windowHours
		cost.NetworkEgressGB = float64(metrics.NetworkEgressBytes) / float64(GB)
		cost.NetworkCrossAZGB = float64(metrics.NetworkCrossAZBytes) / float64(GB)
		cost.NetworkInternetGB = float64(metrics.NetworkInternetBytes) / float64(GB)
		cost.DiskReadMIOPS = float64(metrics.DiskReadIOPS) / 1e6
		cost.DiskWriteMIOPS = float64(metrics.DiskWriteIOPS) / 1e6

		// Apply pricing
		cost.CPUCost = cost.CPUCyclesBillion * prices.CPUCostPerBillionCyclesHour * windowHours
		cost.MemoryCost = cost.MemoryGBHours * prices.MemoryCostPerGBHour
		cost.NetworkCost = (cost.NetworkInternetGB * prices.NetworkInternetEgressPerGB) +
			(cost.NetworkCrossAZGB * prices.NetworkCrossAZPerGB)
		cost.DiskCost = (cost.DiskReadMIOPS * prices.DiskReadCostPerMIOPS) +
			(cost.DiskWriteMIOPS * prices.DiskWriteCostPerMIOPS)
		cost.TotalCost = cost.CPUCost + cost.MemoryCost + cost.NetworkCost + cost.DiskCost

		// Waste calculation
		if metrics.CPURequestedMillis > 0 {
			actualMillis := cost.CPUCyclesBillion * 1000 // rough vCPU estimate
			cost.CPUUtilizationPct = actualMillis / float64(metrics.CPURequestedMillis) * 100
			cost.WastedDollars += cost.CPUCost * (1 - cost.CPUUtilizationPct/100)
		}

		newCosts[key] = cost
	}

	// Build snapshot
	snap := &Snapshot{
		Timestamp: now,
		TeamCosts: make(map[string]*TeamCost),
	}

	for _, cost := range newCosts {
		snap.PodCosts = append(snap.PodCosts, cost)
		snap.Total += cost.TotalCost

		team := cost.Team
		if _, ok := snap.TeamCosts[team]; !ok {
			snap.TeamCosts[team] = &TeamCost{Team: team}
		}
		tc := snap.TeamCosts[team]
		tc.TotalCost += cost.TotalCost
		tc.WastedCost += cost.WastedDollars
		tc.PodCount++
		tc.Pods = append(tc.Pods, cost)
	}

	a.mu.Lock()
	a.podCosts = newCosts
	a.history = append(a.history, snap)
	// Keep last 24 hours of snapshots (at 15s intervals = 5760 snapshots)
	if len(a.history) > 5760 {
		a.history = a.history[1:]
	}
	a.mu.Unlock()
}

// LatestSnapshot returns the most recent cost snapshot.
func (a *Aggregator) LatestSnapshot() *Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.history) == 0 {
		return &Snapshot{Timestamp: time.Now(), TeamCosts: make(map[string]*TeamCost)}
	}
	return a.history[len(a.history)-1]
}

// History returns snapshots within the given time range.
func (a *Aggregator) History(since time.Duration) []*Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	cutoff := time.Now().Add(-since)
	var result []*Snapshot
	for _, s := range a.history {
		if s.Timestamp.After(cutoff) {
			result = append(result, s)
		}
	}
	return result
}

// WasteReport returns pods with < 20% utilization (wasted spend).
func (a *Aggregator) WasteReport() []*PodCost {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var wasted []*PodCost
	for _, cost := range a.podCosts {
		if cost.CPUUtilizationPct > 0 && cost.CPUUtilizationPct < 20 {
			wasted = append(wasted, cost)
		}
	}
	return wasted
}
