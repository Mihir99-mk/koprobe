// Koprobe - Kubernetes Cost Attribution via eBPF
// Real kernel-level cost measurement for K8s workloads.
//
// Usage:
//   koprobe --cloud aws --region us-east-1 --kubeconfig ~/.kube/config

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Mihir99-mk/koprobe/internal/aggregator"
	"github.com/Mihir99-mk/koprobe/internal/collector"
	"github.com/Mihir99-mk/koprobe/internal/enricher"
	"github.com/Mihir99-mk/koprobe/internal/exporter"
	"github.com/Mihir99-mk/koprobe/internal/pricing"
)

const banner = `
██╗  ██╗██╗   ██╗██████╗ ███████╗███████╗██╗███╗   ██╗██████╗ ██████╗ ███████╗
██║ ██╔╝██║   ██║██╔══██╗██╔════╝██╔════╝██║████╗  ██║██╔══██╗██╔══██╗██╔════╝
█████╔╝ ██║   ██║██████╔╝█████╗  █████╗  ██║██╔██╗ ██║██████╔╝██████╔╝█████╗  
██╔═██╗ ██║   ██║██╔══██╗██╔══╝  ██╔══╝  ██║██║╚██╗██║██╔══██╗██╔═══╝ ██╔══╝  
██║  ██╗╚██████╔╝██████╔╝███████╗██║     ██║██║ ╚████║██████╔╝██║     ██║     
╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚══════╝╚═╝     ╚═╝╚═╝  ╚═══╝╚═════╝ ╚═╝     ╚═╝     

  Kubernetes Cost Attribution via eBPF  |  Real usage. Real cost.
`

type Config struct {
	CloudProvider string
	Region        string
	KubeConfig    string
	MetricsPort   int
	APIPort       int
	LogLevel      string
	DryRun        bool
	SlackWebhook  string
	CollectCPU    bool
	CollectNet    bool
	CollectDisk   bool
	CollectMem    bool
}

func main() {
	fmt.Print(banner)

	cfg := parseFlags()

	if os.Geteuid() != 0 {
		log.Fatal("❌ Koprobe requires root privileges to load eBPF programs")
	}

	log.Printf("🚀 Starting Koprobe | cloud=%s region=%s", cfg.CloudProvider, cfg.Region)

	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 1. Initialize K8s enricher (cgroup → pod → team mapping)
	log.Println("🔍 Connecting to Kubernetes API...")
	k8sEnricher, err := enricher.New(cfg.KubeConfig)
	if err != nil {
		log.Fatalf("❌ Failed to connect to K8s: %v", err)
	}

	// 2. Load cloud pricing
	log.Printf("💰 Loading %s pricing data...", cfg.CloudProvider)
	pricer, err := pricing.New(cfg.CloudProvider, cfg.Region)
	if err != nil {
		log.Fatalf("❌ Failed to load pricing: %v", err)
	}

	// 3. Start eBPF collectors (one per resource type)
	log.Println("🔬 Loading eBPF programs into kernel...")
	collectors := collector.NewManager()

	if cfg.CollectCPU {
		if err := collectors.StartCPU(ctx); err != nil {
			log.Fatalf("❌ CPU collector failed: %v", err)
		}
		log.Println("  ✅ CPU cycles collector attached")
	}

	if cfg.CollectNet {
		if err := collectors.StartNetwork(ctx); err != nil {
			log.Fatalf("❌ Network collector failed: %v", err)
		}
		log.Println("  ✅ Network bytes collector attached (TC egress/ingress)")
	}

	if cfg.CollectDisk {
		if err := collectors.StartDisk(ctx); err != nil {
			log.Fatalf("❌ Disk collector failed: %v", err)
		}
		log.Println("  ✅ Disk I/O collector attached (block tracepoints)")
	}

	if cfg.CollectMem {
		if err := collectors.StartMemory(ctx); err != nil {
			log.Fatalf("❌ Memory collector failed: %v", err)
		}
		log.Println("  ✅ Memory pages collector attached")
	}

	// 4. Start aggregator (eBPF data → enriched cost records)
	log.Println("📊 Starting cost aggregator...")
	agg := aggregator.New(collectors, k8sEnricher, pricer)
	go agg.Run(ctx)

	// 5. Start exporters
	log.Printf("📡 Starting Prometheus metrics on :%d/metrics", cfg.MetricsPort)
	promExporter := exporter.NewPrometheus(agg, cfg.MetricsPort)
	go promExporter.Start(ctx)

	log.Printf("🌐 Starting REST API on :%d", cfg.APIPort)
	apiServer := exporter.NewAPIServer(agg, cfg.APIPort)
	go apiServer.Start(ctx)

	if cfg.SlackWebhook != "" {
		log.Println("💬 Slack alerts enabled")
		slackExporter := exporter.NewSlack(agg, cfg.SlackWebhook)
		go slackExporter.StartWeeklyDigest(ctx)
		go slackExporter.StartAnomalyAlerts(ctx)
	}

	log.Println("")
	log.Println("✅ Koprobe is running!")
	log.Printf("   Metrics: http://localhost:%d/metrics", cfg.MetricsPort)
	log.Printf("   API:     http://localhost:%d/api/v1", cfg.APIPort)
	log.Println("   Press Ctrl+C to stop")

	<-ctx.Done()
	log.Println("🛑 Shutting down Koprobe...")
	collectors.Stop()
	log.Println("👋 Goodbye!")
}

func parseFlags() *Config {
	cfg := &Config{}

	flag.StringVar(&cfg.CloudProvider, "cloud", "aws", "Cloud provider: aws, gcp, azure")
	flag.StringVar(&cfg.Region, "region", "us-east-1", "Cloud region")
	flag.StringVar(&cfg.KubeConfig, "kubeconfig", "", "Path to kubeconfig (default: in-cluster)")
	flag.IntVar(&cfg.MetricsPort, "metrics-port", 9090, "Prometheus metrics port")
	flag.IntVar(&cfg.APIPort, "api-port", 8080, "REST API port")
	flag.StringVar(&cfg.LogLevel, "log-level", "info", "Log level: debug, info, warn, error")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Run without loading eBPF programs (simulate)")
	flag.StringVar(&cfg.SlackWebhook, "slack-webhook", "", "Slack webhook URL for alerts")
	flag.BoolVar(&cfg.CollectCPU, "collect-cpu", true, "Enable CPU cycle collection")
	flag.BoolVar(&cfg.CollectNet, "collect-network", true, "Enable network bytes collection")
	flag.BoolVar(&cfg.CollectDisk, "collect-disk", true, "Enable disk I/O collection")
	flag.BoolVar(&cfg.CollectMem, "collect-memory", true, "Enable memory pages collection")

	flag.Parse()
	return cfg
}
