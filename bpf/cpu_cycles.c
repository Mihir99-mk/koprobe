// SPDX-License-Identifier: GPL-2.0
// Koprobe - CPU Cycles per cgroup (pod) tracker
//
// Attaches to perf events and measures actual CPU cycles
// consumed per cgroup ID, which maps 1:1 to K8s containers.

#include "headers/common.h"

// Map: cgroup_id → cpu_stats
struct cpu_stats {
    __u64 cycles;
    __u64 instructions;
    __u64 task_clock_ns;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key, __u64);              // cgroup_id
    __type(value, struct cpu_stats);
} cpu_cycles_map SEC(".maps");

// Ringbuf for sending events to userspace
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 24);   // 16MB ring buffer
} cpu_events SEC(".maps");

struct cpu_event {
    __u64 cgroup_id;
    __u64 cycles;
    __u64 timestamp_ns;
    __u32 pid;
    char comm[16];
};

// Fires on every perf CPU cycle sample
SEC("perf_event")
int measure_cpu_cycles(struct bpf_perf_event_data *ctx)
{
    __u64 cgroup_id = bpf_get_current_cgroup_id();
    struct cpu_stats *stats;
    struct cpu_stats init_stats = {};

    stats = bpf_map_lookup_elem(&cpu_cycles_map, &cgroup_id);
    if (stats) {
        __sync_fetch_and_add(&stats->cycles, 1);
        __sync_fetch_and_add(&stats->task_clock_ns, ctx->sample_period);
    } else {
        init_stats.cycles = 1;
        init_stats.task_clock_ns = ctx->sample_period;
        bpf_map_update_elem(&cpu_cycles_map, &cgroup_id, &init_stats, BPF_ANY);
    }

    // Send high-level event every N samples to avoid ringbuf flood
    if ((bpf_ktime_get_ns() % 1000000000) < 10000000) { // ~1% sampling
        struct cpu_event *event;
        event = bpf_ringbuf_reserve(&cpu_events, sizeof(*event), 0);
        if (event) {
            event->cgroup_id = cgroup_id;
            event->cycles = stats ? stats->cycles : 1;
            event->timestamp_ns = bpf_ktime_get_ns();
            event->pid = bpf_get_current_pid_tgid() >> 32;
            bpf_get_current_comm(&event->comm, sizeof(event->comm));
            bpf_ringbuf_submit(event, 0);
        }
    }

    return 0;
}

char LICENSE[] SEC("license") = "GPL";
