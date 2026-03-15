# ─── Build Stage ───────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache \
    clang \
    llvm \
    libbpf-dev \
    linux-headers \
    make \
    git

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN make build-ebpf   # compile eBPF C → .o files
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=$(git describe --tags --always)" \
    -o koprobe ./cmd/koprobe

# ─── Runtime Stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache \
    libbpf \
    libelf \
    ca-certificates

# eBPF programs need to be accessible at runtime
COPY --from=builder /build/bpf/*.o /etc/koprobe/bpf/
COPY --from=builder /build/koprobe /usr/local/bin/koprobe

# eBPF requires privileged access (CAP_BPF, CAP_NET_ADMIN)
# These are set in the Helm chart's securityContext

ENTRYPOINT ["/usr/local/bin/koprobe"]
