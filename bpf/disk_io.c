// SPDX-License-Identifier: GPL-2.0
// Koprobe - Disk I/O per pod tracker
//
// Hooks into block layer tracepoints to measure
// actual disk IOPS and throughput per cgroup/pod.

#include "headers/common.h"

struct disk_stats {
    __u64 read_bytes;
    __u64 write_bytes;
    __u64 read_iops;
    __u64 write_iops;
    __u64 read_latency_ns;   // cumulative, divide by read_iops for avg
    __u64 write_latency_ns;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key, __u64);              // cgroup_id
    __type(value, struct disk_stats);
} disk_map SEC(".maps");

// Track in-flight I/O requests for latency calculation
struct io_request {
    __u64 start_ns;
    __u64 cgroup_id;
    __u32 size;
    __u8  is_write;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 65536);
    __type(key, __u64);              // request pointer
    __type(value, struct io_request);
} inflight_map SEC(".maps");

// Hook when I/O is submitted to block layer
SEC("tracepoint/block/block_rq_issue")
int trace_rq_issue(struct trace_event_raw_block_rq *ctx)
{
    __u64 cgroup_id = bpf_get_current_cgroup_id();
    __u64 req_ptr = (__u64)ctx->sector; // use sector as unique key

    struct io_request req = {
        .start_ns  = bpf_ktime_get_ns(),
        .cgroup_id = cgroup_id,
        .size      = ctx->nr_sector * 512,
        .is_write  = (ctx->rwbs[0] == 'W') ? 1 : 0,
    };

    bpf_map_update_elem(&inflight_map, &req_ptr, &req, BPF_ANY);
    return 0;
}

// Hook when I/O completes
SEC("tracepoint/block/block_rq_complete")
int trace_rq_complete(struct trace_event_raw_block_rq_completion *ctx)
{
    __u64 req_ptr = (__u64)ctx->sector;
    struct io_request *req = bpf_map_lookup_elem(&inflight_map, &req_ptr);
    if (!req) return 0;

    __u64 latency = bpf_ktime_get_ns() - req->start_ns;
    __u64 cgroup_id = req->cgroup_id;
    __u32 size = req->size;
    __u8 is_write = req->is_write;

    bpf_map_delete_elem(&inflight_map, &req_ptr);

    struct disk_stats *stats = bpf_map_lookup_elem(&disk_map, &cgroup_id);
    struct disk_stats init = {};

    if (!stats) {
        bpf_map_update_elem(&disk_map, &cgroup_id, &init, BPF_ANY);
        stats = bpf_map_lookup_elem(&disk_map, &cgroup_id);
        if (!stats) return 0;
    }

    if (is_write) {
        __sync_fetch_and_add(&stats->write_bytes, size);
        __sync_fetch_and_add(&stats->write_iops, 1);
        __sync_fetch_and_add(&stats->write_latency_ns, latency);
    } else {
        __sync_fetch_and_add(&stats->read_bytes, size);
        __sync_fetch_and_add(&stats->read_iops, 1);
        __sync_fetch_and_add(&stats->read_latency_ns, latency);
    }

    return 0;
}

char LICENSE[] SEC("license") = "GPL";
