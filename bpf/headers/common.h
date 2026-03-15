/* SPDX-License-Identifier: GPL-2.0 */
/* Koprobe - Common headers for all eBPF programs */

#pragma once

#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/tcp.h>
#include <linux/udp.h>
#include <linux/pkt_cls.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>

/* Convenience macros */
#define BPF_ANNOTATE_KV_PAIR(name, type_key, type_val) \
    typeof(type_key) *__##name##_key;                   \
    typeof(type_val) *__##name##_val;

#define TASK_COMM_LEN 16
#define MAX_CPUS      256

/* Cgroup v2 path max length */
#define CGROUP_PATH_LEN 256

/* Cost label keys used across all maps */
#define LABEL_NAMESPACE  0
#define LABEL_POD        1
#define LABEL_TEAM       2
#define LABEL_SERVICE    3
#define LABEL_FEATURE    4

/* Traffic direction */
#define DIRECTION_INGRESS  0
#define DIRECTION_EGRESS   1
#define DIRECTION_CROSS_AZ 2
#define DIRECTION_INTERNET 3

/* Shared timestamp helper */
static __always_inline __u64 get_ns(void)
{
    return bpf_ktime_get_ns();
}
