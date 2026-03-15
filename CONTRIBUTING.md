# Contributing to Koprobe

Thanks for your interest! Koprobe welcomes contributions of all kinds.

## Getting Started

### Prerequisites
- Linux (eBPF programs only run on Linux)
- Go 1.22+
- clang + llvm (for compiling eBPF C programs)
- A Kubernetes cluster (kind works great for local dev)

```bash
# Ubuntu/Debian
sudo apt-get install -y clang llvm libbpf-dev linux-headers-$(uname -r)

# Arch
sudo pacman -S clang llvm libbpf linux-headers
```

### Local Setup
```bash
git clone https://github.com/Mihir99-mk/koprobe
cd koprobe

# Compile eBPF programs
make build-ebpf

# Build the binary
make build

# Run tests
make test

# Run locally (dry-run, no root needed)
make dev
```

## How to Contribute

### 🐛 Bug Reports
Open an issue with:
- Koprobe version (`koprobe --version`)
- Kernel version (`uname -r`)
- Cloud provider + region
- Steps to reproduce
- Expected vs actual behavior

### 💡 Feature Requests
Open an issue describing:
- The problem you're solving
- Your proposed solution
- Alternatives you considered

### 🔧 Pull Requests

1. Fork the repo
2. Create a branch: `git checkout -b feat/my-feature`
3. Make your changes
4. Add tests for new functionality
5. Run `make lint test`
6. Commit with conventional commits: `feat:`, `fix:`, `docs:`
7. Open a PR against `main`

### Good First Issues
Look for issues labeled `good first issue` — these are intentionally
scoped to be approachable for new contributors.

## Project Structure

```
bpf/          eBPF C programs (kernel-space)
cmd/          CLI entrypoint
internal/
  collector/  Load eBPF programs + read maps
  enricher/   cgroup ID → K8s pod metadata
  pricing/    Cloud pricing APIs
  aggregator/ Cost calculation engine
  exporter/   Prometheus, REST API, Slack
deploy/
  helm/       Helm chart
  docker/     Dockerfile
```

## eBPF Development Tips

- Test eBPF programs with `bpftool prog` and `bpftool map`
- Use `bpf_printk()` for debug output (visible in `/sys/kernel/debug/tracing/trace_pipe`)
- Always verify programs compile before submitting: `make build-ebpf`
- The verifier is strict — avoid unbounded loops and always check pointer bounds

## Code Style

- Go: follow `gofmt` + `golangci-lint` rules
- eBPF C: follow kernel coding style (tabs, 80 col)
- Commits: use [Conventional Commits](https://conventionalcommits.org)

## License

By contributing, you agree your contributions will be licensed under Apache 2.0.
