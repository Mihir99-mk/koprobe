# Koprobe Makefile

BINARY     := koprobe
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS    := -s -w \
              -X main.version=$(VERSION) \
              -X main.commit=$(COMMIT) \
              -X main.buildDate=$(BUILD_DATE)

# Directories
BPF_DIR    := ./bpf
OUTPUT_DIR := ./dist
CLANG      ?= clang
CLANG_FLAGS := -O2 -g -target bpf -D__TARGET_ARCH_x86

.PHONY: all build build-ebpf test lint clean docker helm-package dev help

all: build-ebpf build ## Build everything

## ─── eBPF ──────────────────────────────────────────────────────────────────────

build-ebpf: ## Compile eBPF C programs to .o files
	@echo "🔬 Compiling eBPF programs..."
	@mkdir -p $(BPF_DIR)/out
	$(CLANG) $(CLANG_FLAGS) \
		-I/usr/include/x86_64-linux-gnu \
		-I$(BPF_DIR)/headers \
		-c $(BPF_DIR)/cpu_cycles.c \
		-o $(BPF_DIR)/out/cpu_cycles.o
	$(CLANG) $(CLANG_FLAGS) \
		-I/usr/include/x86_64-linux-gnu \
		-I$(BPF_DIR)/headers \
		-c $(BPF_DIR)/network_bytes.c \
		-o $(BPF_DIR)/out/network_bytes.o
	$(CLANG) $(CLANG_FLAGS) \
		-I/usr/include/x86_64-linux-gnu \
		-I$(BPF_DIR)/headers \
		-c $(BPF_DIR)/disk_io.c \
		-o $(BPF_DIR)/out/disk_io.o
	@echo "✅ eBPF programs compiled → $(BPF_DIR)/out/"

generate-go: build-ebpf ## Generate Go bindings from eBPF .o files (bpf2go)
	@echo "⚙️  Generating Go bindings..."
	go generate ./internal/collector/...

## ─── Go ────────────────────────────────────────────────────────────────────────

build: ## Build the Go binary
	@echo "🔨 Building $(BINARY) $(VERSION)..."
	@mkdir -p $(OUTPUT_DIR)
	CGO_ENABLED=0 GOOS=linux go build \
		-ldflags="$(LDFLAGS)" \
		-o $(OUTPUT_DIR)/$(BINARY) \
		./cmd/$(BINARY)
	@echo "✅ Binary → $(OUTPUT_DIR)/$(BINARY)"

build-all: ## Build for linux/amd64 and linux/arm64
	@mkdir -p $(OUTPUT_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" \
		-o $(OUTPUT_DIR)/$(BINARY)-linux-amd64 ./cmd/$(BINARY)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" \
		-o $(OUTPUT_DIR)/$(BINARY)-linux-arm64 ./cmd/$(BINARY)
	cd $(OUTPUT_DIR) && sha256sum * > checksums.txt
	@echo "✅ Multi-arch binaries → $(OUTPUT_DIR)/"

test: ## Run all tests
	@echo "🧪 Running tests..."
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "✅ Coverage report → coverage.html"

test-ebpf: ## Run eBPF integration tests (requires root + Linux)
	@echo "🧪 Running eBPF integration tests (requires root)..."
	sudo go test -v -tags=integration ./internal/collector/...

lint: ## Run linters
	@echo "🔍 Linting..."
	go vet ./...
	golangci-lint run ./...

fmt: ## Format code
	gofmt -s -w .
	goimports -w .

## ─── Docker ────────────────────────────────────────────────────────────────────

docker-build: ## Build Docker image
	@echo "🐳 Building Docker image..."
	docker build \
		-t koprobe:$(VERSION) \
		-t koprobe:latest \
		-f deploy/docker/Dockerfile \
		.

docker-push: ## Push to GHCR
	docker tag koprobe:$(VERSION) ghcr.io/koprobe/koprobe:$(VERSION)
	docker tag koprobe:latest ghcr.io/koprobe/koprobe:latest
	docker push ghcr.io/koprobe/koprobe:$(VERSION)
	docker push ghcr.io/koprobe/koprobe:latest

## ─── Helm ──────────────────────────────────────────────────────────────────────

helm-lint: ## Lint the Helm chart
	helm lint deploy/helm

helm-package: ## Package the Helm chart
	@mkdir -p $(OUTPUT_DIR)
	helm package deploy/helm --destination $(OUTPUT_DIR)/

helm-install: ## Install into current K8s context (for dev)
	helm upgrade --install koprobe ./deploy/helm \
		--namespace monitoring \
		--create-namespace \
		--set cloud.provider=aws \
		--set cloud.region=us-east-1 \
		--set image.tag=$(VERSION) \
		--wait

helm-uninstall: ## Uninstall from current K8s context
	helm uninstall koprobe --namespace monitoring

## ─── Dev ───────────────────────────────────────────────────────────────────────

dev: ## Run locally in dry-run mode (no K8s/eBPF required)
	go run ./cmd/$(BINARY) \
		--dry-run \
		--cloud=aws \
		--region=us-east-1 \
		--log-level=debug

kind-cluster: ## Create a local kind cluster for testing
	kind create cluster --name koprobe-dev
	kubectl cluster-info --context kind-koprobe-dev

clean: ## Remove build artifacts
	rm -rf $(OUTPUT_DIR) $(BPF_DIR)/out coverage.out coverage.html

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  Version: $(VERSION) | Commit: $(COMMIT)"
