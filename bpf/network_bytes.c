// SPDX-License-Identifier: GPL-2.0
// Koprobe - Network bytes per pod tracker
//
// Hooks into TC (Traffic Control) egress/ingress
// to count bytes per cgroup/pod with direction awareness.
// Also tracks cross-AZ traffic for accurate cost attribution.

#include "headers/common.h"

struct pod_traffic {
    __u64 bytes_ingress;
    __u64 bytes_egress;
    __u64 packets_ingress;
    __u64 packets_egress;
    __u64 bytes_cross_az;    // expensive cross-zone traffic
    __u64 bytes_internet;    // internet egress (most expensive)
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key, __u64);                  // cgroup_id
    __type(value, struct pod_traffic);
} network_map SEC(".maps");

// Known subnet ranges for AZ detection (populated from userspace)
struct az_subnet {
    __u32 subnet;
    __u32 mask;
    __u8  az_id;
};

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 64);
    __type(key, __u32);
    __type(value, struct az_subnet);
} az_subnets SEC(".maps");

// Node's own AZ ID (set from userspace on startup)
struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, __u8);
} node_az_id SEC(".maps");

static __always_inline __u8 is_cross_az(__u32 dest_ip)
{
    __u32 key = 0;
    __u8 *my_az = bpf_map_lookup_elem(&node_az_id, &key);
    if (!my_az) return 0;

    for (__u32 i = 0; i < 64; i++) {
        struct az_subnet *subnet = bpf_map_lookup_elem(&az_subnets, &i);
        if (!subnet || subnet->subnet == 0) break;
        if ((dest_ip & subnet->mask) == subnet->subnet) {
            return subnet->az_id != *my_az;
        }
    }
    return 0;
}

static __always_inline __u8 is_internet(__u32 dest_ip)
{
    // RFC1918 private ranges
    if ((dest_ip & 0xFF000000) == 0x0A000000) return 0; // 10.0.0.0/8
    if ((dest_ip & 0xFFF00000) == 0xAC100000) return 0; // 172.16.0.0/12
    if ((dest_ip & 0xFFFF0000) == 0xC0A80000) return 0; // 192.168.0.0/16
    return 1;
}

SEC("tc/egress")
int count_egress(struct __sk_buff *skb)
{
    __u64 cgroup_id = bpf_skb_cgroup_id(skb);
    if (cgroup_id == 0) return TC_ACT_OK;

    struct pod_traffic *t = bpf_map_lookup_elem(&network_map, &cgroup_id);
    struct pod_traffic init = {};

    if (!t) {
        bpf_map_update_elem(&network_map, &cgroup_id, &init, BPF_ANY);
        t = bpf_map_lookup_elem(&network_map, &cgroup_id);
        if (!t) return TC_ACT_OK;
    }

    __sync_fetch_and_add(&t->bytes_egress, skb->len);
    __sync_fetch_and_add(&t->packets_egress, 1);

    // Parse destination IP for cost categorization
    void *data = (void *)(long)skb->data;
    void *data_end = (void *)(long)skb->data_end;
    struct iphdr *ip = data + sizeof(struct ethhdr);

    if ((void *)(ip + 1) <= data_end) {
        __u32 dest = bpf_ntohl(ip->daddr);
        if (is_internet(dest)) {
            __sync_fetch_and_add(&t->bytes_internet, skb->len);
        } else if (is_cross_az(dest)) {
            __sync_fetch_and_add(&t->bytes_cross_az, skb->len);
        }
    }

    return TC_ACT_OK;  // never drop — only measure
}

SEC("tc/ingress")
int count_ingress(struct __sk_buff *skb)
{
    __u64 cgroup_id = bpf_skb_cgroup_id(skb);
    if (cgroup_id == 0) return TC_ACT_OK;

    struct pod_traffic *t = bpf_map_lookup_elem(&network_map, &cgroup_id);
    struct pod_traffic init = {};

    if (!t) {
        bpf_map_update_elem(&network_map, &cgroup_id, &init, BPF_ANY);
        t = bpf_map_lookup_elem(&network_map, &cgroup_id);
        if (!t) return TC_ACT_OK;
    }

    __sync_fetch_and_add(&t->bytes_ingress, skb->len);
    __sync_fetch_and_add(&t->packets_ingress, 1);
    return TC_ACT_OK;
}

char LICENSE[] SEC("license") = "GPL";
