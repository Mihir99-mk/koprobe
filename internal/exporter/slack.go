package exporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/Mihir99-mk/koprobe/internal/aggregator"
)

// SlackExporter sends cost alerts and weekly digests to Slack.
type SlackExporter struct {
	agg        *aggregator.Aggregator
	webhookURL string
	client     *http.Client

	// Anomaly detection: track baselines per team
	teamBaselines map[string]float64
}

func NewSlack(agg *aggregator.Aggregator, webhookURL string) *SlackExporter {
	return &SlackExporter{
		agg:           agg,
		webhookURL:    webhookURL,
		client:        &http.Client{Timeout: 10 * time.Second},
		teamBaselines: make(map[string]float64),
	}
}

// StartWeeklyDigest sends a cost summary every Monday at 9am.
func (s *SlackExporter) StartWeeklyDigest(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			if t.Weekday() == time.Monday && t.Hour() == 9 {
				s.sendWeeklyDigest()
			}
		}
	}
}

// StartAnomalyAlerts checks for cost spikes every 5 minutes.
func (s *SlackExporter) StartAnomalyAlerts(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkAnomalies()
		}
	}
}

func (s *SlackExporter) checkAnomalies() {
	snap := s.agg.LatestSnapshot()

	for team, tc := range snap.TeamCosts {
		baseline, ok := s.teamBaselines[team]
		if !ok {
			s.teamBaselines[team] = tc.TotalCost
			continue
		}

		// Alert if cost is >200% of baseline
		if baseline > 0 && tc.TotalCost > baseline*2.0 {
			pct := ((tc.TotalCost - baseline) / baseline) * 100
			s.sendAlert(fmt.Sprintf(
				"🔴 *Cost Spike Detected*\n"+
					"Team: *%s*\n"+
					"Current: $%.2f (↑ %.0f%% above baseline)\n"+
					"Baseline: $%.2f\n"+
					"Check: /api/v1/costs/by-pod?team=%s",
				team, tc.TotalCost, pct, baseline, team,
			))
		}

		// Update rolling baseline (exponential moving average)
		s.teamBaselines[team] = (baseline*0.9 + tc.TotalCost*0.1)
	}
}

func (s *SlackExporter) sendWeeklyDigest() {
	snap := s.agg.LatestSnapshot()

	// Sort teams by cost
	type teamEntry struct {
		Name string
		Cost float64
	}
	var teams []teamEntry
	for name, tc := range snap.TeamCosts {
		teams = append(teams, teamEntry{name, tc.TotalCost})
	}
	sort.Slice(teams, func(i, j int) bool { return teams[i].Cost > teams[j].Cost })

	// Build message
	msg := fmt.Sprintf("📊 *Koprobe Weekly Cost Report* — %s\n\n", time.Now().Format("Jan 2, 2006"))
	msg += fmt.Sprintf("*Total Cluster Cost (last 24h):* $%.2f\n\n", snap.Total)
	msg += "*Top Spenders:*\n"
	for i, t := range teams {
		if i >= 5 {
			break
		}
		bar := buildBar(t.Cost, snap.Total)
		msg += fmt.Sprintf("  %s `%s` $%.2f\n", bar, t.Name, t.Cost)
	}

	// Waste report
	wasted := s.agg.WasteReport()
	if len(wasted) > 0 {
		var totalWaste float64
		for _, p := range wasted {
			totalWaste += p.WastedDollars
		}
		msg += fmt.Sprintf("\n💸 *Wasted Spend:* $%.2f across %d pods\n", totalWaste, len(wasted))
		for i, p := range wasted {
			if i >= 3 {
				break
			}
			msg += fmt.Sprintf("  • `%s/%s` — %.0f%% CPU util, $%.2f/window wasted\n",
				p.Namespace, p.PodName, p.CPUUtilizationPct, p.WastedDollars)
		}
	}

	s.sendAlert(msg)
}

func (s *SlackExporter) sendAlert(text string) {
	payload := map[string]string{"text": text}
	body, _ := json.Marshal(payload)
	s.client.Post(s.webhookURL, "application/json", bytes.NewReader(body))
}

func buildBar(value, total float64) string {
	if total == 0 {
		return "░░░░░░░░░░"
	}
	pct := value / total
	filled := int(pct * 10)
	bar := ""
	for i := 0; i < 10; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	return bar
}
