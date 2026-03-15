# Koprobe 🔬💰

> **Real Kubernetes cost attribution via eBPF.**  
> Know exactly what your workloads cost — down to the pod, team, and feature.  
> Measured at the kernel level. Not estimated from resource requests.

[![CI](https://github.com/Mihir99-mk/koprobe/actions/workflows/ci.yml/badge.svg)](https://github.com/Mihir99-mk/koprobe/actions)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8.svg)](https://golang.org)
[![eBPF](https://img.shields.io/badge/powered%20by-eBPF-orange)](https://ebpf.io)

---

## The Problem

Every FinOps tool today estimates costs from **requested** CPU and memory.

```
Pod requests 4 CPU, uses 0.2 CPU → billed as if it used 4 CPU ❌
```

Koprobe measures **actual usage** from the Linux kernel via eBPF:

```
Pod requests 4 CPU, uses 0.2 CPU → billed for 0.2 CPU ✅
```

This means you can finally answer:
- *"Which team is actually responsible for that $40k spike?"*
- *"How much of our cloud bill is genuinely wasted?"*
- *"What does this specific feature cost per request?"*

---

## How It Works

```
┌─────────────────────────────────────────────────────┐
│                   K8s Node                          │
│                                                     │
│  Pod A   Pod B   Pod C                              │
│   │        │       │                               │
│   └────────┴───────┘                               │
│            │                                       │
│     [ eBPF Programs ]                              │
│     ├─ cpu_cycles.c    → actual CPU cycles/pod     │
│     ├─ network_bytes.c → bytes in/out/cross-AZ     │
│     ├─ disk_io.c       → IOPS + latency/pod        │
│     └─ memory cgroup   → actual memory pages       │
│            │                                       │
│     [ Go Aggregator ]                              │
│     ├─ Enrich: cgroup_id → pod → team → feature   │
│     ├─ Price:  AWS/GCP/Azure pricing API           │
│     └─ Emit:  Prometheus + REST API + Slack        │
└─────────────────────────────────────────────────────┘
```

Overhead: **< 0.5% CPU** (in-kernel aggregation, no sampling).

---

## Quick Start

### Install with Helm (recommended)

```bash
helm repo add koprobe https://koprobe.github.io/koprobe
helm repo update

helm install koprobe koprobe/koprobe \
  --namespace monitoring \
  --create-namespace \
  --set cloud.provider=aws \
  --set cloud.region=us-east-1
```

### Access the dashboard

```bash
kubectl port-forward svc/koprobe-metrics 8080:8080 -n monitoring
open http://localhost:8080
```

### Install the binary

```bash
curl -Lo koprobe \
  https://github.com/Mihir99-mk/koprobe/releases/latest/download/koprobe-linux-amd64
chmod +x koprobe && sudo mv koprobe /usr/local/bin/

# Run (requires root for eBPF)
sudo koprobe --cloud=aws --region=us-east-1
```

---

## Cost Attribution Labels

Koprobe reads standard K8s labels to attribute costs:

```yaml
# Add these labels to your pods/deployments
metadata:
  labels:
    app.kubernetes.io/team: "backend"        # required
    app.kubernetes.io/name: "payment-api"    # service name
    feature: "checkout"                       # optional
    environment: "production"                 # optional
    billing/cost-center: "eng-platform"       # optional
```

---

## Metrics

Koprobe exposes Prometheus metrics at `:9090/metrics`:

```
# Per-pod costs (15s window)
kubefinbpf_pod_cost_total{namespace,pod,team,service,env}
kubefinbpf_pod_cpu_cost{...}
kubefinbpf_pod_memory_cost{...}
kubefinbpf_pod_network_cost{...}
kubefinbpf_pod_disk_cost{...}
kubefinbpf_pod_wasted_dollars{...}
kubefinbpf_pod_cpu_utilization_pct{...}

# Per-team aggregates
kubefinbpf_team_cost_total{team}
kubefinbpf_team_wasted_dollars{team}

# Cluster total
kubefinbpf_cluster_cost_total
```

---

## REST API

```bash
# Cost summary
GET /api/v1/costs/summary

# By team (sorted by spend)
GET /api/v1/costs/by-team

# By namespace
GET /api/v1/costs/by-namespace

# By pod (filter by team or namespace)
GET /api/v1/costs/by-pod?team=backend
GET /api/v1/costs/by-pod?namespace=production

# Waste report (pods < 20% utilization)
GET /api/v1/costs/waste

# Cost history (last 24h by default)
GET /api/v1/costs/history?hours=48
```

---

## Slack Alerts

```bash
helm upgrade koprobe koprobe/koprobe \
  --set slack.enabled=true \
  --set slack.webhookURL=https://hooks.slack.com/services/...
```

You'll receive:
- **Weekly digest** every Monday 9am with team cost breakdown
- **Spike alerts** when a team's cost exceeds 2x their baseline
- **Waste report** highlighting underutilized pods

---

## vs Kubecost

| | Kubecost | Koprobe |
|--|---------|-----------|
| Measurement method | K8s resource requests | Actual kernel usage via eBPF ✅ |
| Network cost accuracy | Estimated | Measured (incl. cross-AZ) ✅ |
| CPU overhead | ~2-3% | < 0.5% ✅ |
| Open source | Partial | Fully Apache 2.0 ✅ |
| Install complexity | High | `helm install` in 60s ✅ |

---

## Requirements

- Linux kernel **5.8+** (for BPF ring buffer)
- Kubernetes **1.24+**
- cgroup **v2**
- One of: AWS EKS, GCP GKE, Azure AKS, or self-managed K8s

### Required capabilities
```yaml
capabilities:
  add: [BPF, NET_ADMIN, SYS_RESOURCE, PERFMON]
```

---

## Development

```bash
git clone https://github.com/Mihir99-mk/koprobe
cd koprobe

# Install eBPF build deps (Ubuntu/Debian)
sudo apt-get install -y clang llvm libbpf-dev linux-headers-$(uname -r)

# Build everything
make all

# Run tests
make test

# Run locally (dry-run, no root needed)
make dev

# Create local kind cluster
make kind-cluster
make helm-install
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for full contribution guide.

---

## Roadmap

- [x] CPU cycle collection (eBPF perf_event)
- [x] Network bytes collection (TC eBPF)
- [x] Disk I/O collection (block tracepoints)
- [x] K8s cgroup → pod enrichment
- [x] AWS/GCP/Azure pricing
- [x] Prometheus export
- [x] REST API
- [x] Slack alerts
- [x] Helm chart
- [ ] GPU cost attribution
- [ ] Grafana dashboard (pre-built)
- [ ] Cost anomaly ML model
- [ ] Multi-cluster aggregation
- [ ] Web UI dashboard
- [ ] FinOps Foundation FOCUS schema export

---

## Community

- 💬 [Slack](https://koprobe.slack.com) — `#koprobe`
- 🐛 [Issues](https://github.com/Mihir99-mk/koprobe/issues)
- 📖 [Docs](https://koprobe.github.io/koprobe)
- 🐦 [Twitter](https://twitter.com/koprobe)

---

## License

Apache 2.0 — see [LICENSE](LICENSE)

---

<p align="center">
  Built with ❤️ and eBPF
</p>
