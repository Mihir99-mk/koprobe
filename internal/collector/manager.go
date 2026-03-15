// Package collector manages the lifecycle of eBPF programs
// and provides a unified interface to read their maps.
package collector

import (
	"context"
	"sync"
)

// RawMetrics holds raw measurements from all eBPF programs
// for a single cgroup (container/pod).
type RawMetrics struct {
	CgroupID uint64

	// CPU (from perf_event eBPF)
	CPUCycles           uint64
	CPUInstructions     uint64
	CPURequestedMillis  int64 // from K8s resource requests

	// Memory (from memory cgroup)
	MemoryBytesAvg     uint64
	MemoryBytesMax     uint64
	MemoryRequestedMB  int64

	// Network (from TC eBPF)
	NetworkIngressBytes  uint64
	NetworkEgressBytes   uint64
	NetworkCrossAZBytes  uint64
	NetworkInternetBytes uint64
	NetworkPackets       uint64

	// Disk (from block tracepoints)
	DiskReadBytes  uint64
	DiskWriteBytes uint64
	DiskReadIOPS   uint64
	DiskWriteIOPS  uint64
	DiskReadLatNs  uint64
	DiskWriteLatNs uint64
}

// Manager coordinates all eBPF collectors.
type Manager struct {
	mu      sync.RWMutex
	metrics map[uint64]*RawMetrics // cgroup_id → metrics

	cpuCollector     *CPUCollector
	networkCollector *NetworkCollector
	diskCollector    *DiskCollector
	memoryCollector  *MemoryCollector
}

func NewManager() *Manager {
	return &Manager{
		metrics: make(map[uint64]*RawMetrics),
	}
}

func (m *Manager) StartCPU(ctx context.Context) error {
	m.cpuCollector = &CPUCollector{}
	return m.cpuCollector.Start(ctx, m)
}

func (m *Manager) StartNetwork(ctx context.Context) error {
	m.networkCollector = &NetworkCollector{}
	return m.networkCollector.Start(ctx, m)
}

func (m *Manager) StartDisk(ctx context.Context) error {
	m.diskCollector = &DiskCollector{}
	return m.diskCollector.Start(ctx, m)
}

func (m *Manager) StartMemory(ctx context.Context) error {
	m.memoryCollector = &MemoryCollector{}
	return m.memoryCollector.Start(ctx, m)
}

// Snapshot returns a copy of all current metrics.
func (m *Manager) Snapshot() map[uint64]*RawMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[uint64]*RawMetrics, len(m.metrics))
	for k, v := range m.metrics {
		copy := *v
		result[k] = &copy
	}
	return result
}

// UpdateCPU is called by the CPU collector to update metrics.
func (m *Manager) UpdateCPU(cgroupID uint64, cycles, instructions uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.metrics[cgroupID]; !ok {
		m.metrics[cgroupID] = &RawMetrics{CgroupID: cgroupID}
	}
	m.metrics[cgroupID].CPUCycles += cycles
	m.metrics[cgroupID].CPUInstructions += instructions
}

// UpdateNetwork is called by the network collector.
func (m *Manager) UpdateNetwork(cgroupID, ingress, egress, crossAZ, internet uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.metrics[cgroupID]; !ok {
		m.metrics[cgroupID] = &RawMetrics{CgroupID: cgroupID}
	}
	m.metrics[cgroupID].NetworkIngressBytes += ingress
	m.metrics[cgroupID].NetworkEgressBytes += egress
	m.metrics[cgroupID].NetworkCrossAZBytes += crossAZ
	m.metrics[cgroupID].NetworkInternetBytes += internet
}

// UpdateDisk is called by the disk collector.
func (m *Manager) UpdateDisk(cgroupID, readBytes, writeBytes, readIOPS, writeIOPS uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.metrics[cgroupID]; !ok {
		m.metrics[cgroupID] = &RawMetrics{CgroupID: cgroupID}
	}
	m.metrics[cgroupID].DiskReadBytes += readBytes
	m.metrics[cgroupID].DiskWriteBytes += writeBytes
	m.metrics[cgroupID].DiskReadIOPS += readIOPS
	m.metrics[cgroupID].DiskWriteIOPS += writeIOPS
}

// Stop detaches all eBPF programs.
func (m *Manager) Stop() {
	if m.cpuCollector != nil {
		m.cpuCollector.Stop()
	}
	if m.networkCollector != nil {
		m.networkCollector.Stop()
	}
	if m.diskCollector != nil {
		m.diskCollector.Stop()
	}
	if m.memoryCollector != nil {
		m.memoryCollector.Stop()
	}
}

// --- Individual collectors (load actual eBPF programs) ---

// CPUCollector loads and manages the cpu_cycles eBPF program.
type CPUCollector struct{ stopFn context.CancelFunc }

func (c *CPUCollector) Start(ctx context.Context, mgr *Manager) error {
	// In production: use cilium/ebpf to load bpf/cpu_cycles.o
	// and attach to perf_event. For now, scaffold is in place.
	//
	// objs := cpuCyclesObjects{}
	// if err := loadCpuCyclesObjects(&objs, nil); err != nil { return err }
	// events, err := perf.NewReader(objs.CpuCyclesMap, os.Getpagesize())
	//
	// go c.readLoop(ctx, events, mgr)
	return nil
}
func (c *CPUCollector) Stop() {}

// NetworkCollector loads and manages the network_bytes TC program.
type NetworkCollector struct{ stopFn context.CancelFunc }

func (c *NetworkCollector) Start(ctx context.Context, mgr *Manager) error {
	// In production: use cilium/ebpf + netlink to attach TC programs
	// to every network interface on the node.
	return nil
}
func (c *NetworkCollector) Stop() {}

// DiskCollector loads and manages the disk_io tracepoint program.
type DiskCollector struct{ stopFn context.CancelFunc }

func (c *DiskCollector) Start(ctx context.Context, mgr *Manager) error {
	// In production: use cilium/ebpf to load and attach to
	// tracepoint/block/block_rq_issue and block_rq_complete
	return nil
}
func (c *DiskCollector) Stop() {}

// MemoryCollector reads memory usage from cgroup v2 memory files.
type MemoryCollector struct{ stopFn context.CancelFunc }

func (c *MemoryCollector) Start(ctx context.Context, mgr *Manager) error {
	// Reads /sys/fs/cgroup/<pod>/memory.current periodically
	// No eBPF needed for memory — cgroup files are sufficient
	return nil
}
func (c *MemoryCollector) Stop() {}
