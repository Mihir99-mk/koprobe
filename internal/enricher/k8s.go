// Package enricher maps kernel cgroup IDs to K8s pod metadata.
// This is the critical bridge between low-level eBPF measurements
// and human-readable team/service/feature cost attribution.
package enricher

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// PodInfo holds all K8s metadata for a pod, derived from its cgroup ID.
type PodInfo struct {
	PodName      string
	Namespace    string
	NodeName     string
	TeamLabel    string // app.kubernetes.io/team
	ServiceLabel string // app.kubernetes.io/name
	FeatureLabel string // feature or app.kubernetes.io/component
	CostCenter   string // billing/cost-center label
	Environment  string // staging, production, dev
	CgroupID     uint64
	ContainerID  string
}

// Enricher resolves cgroup IDs to PodInfo.
type Enricher struct {
	k8s          kubernetes.Interface
	mu           sync.RWMutex
	cgroupToPod  map[uint64]*PodInfo // cgroup_id → PodInfo
	containerToPod map[string]*PodInfo // containerID → PodInfo
}

// New creates an Enricher and starts a background sync loop.
func New(kubeconfig string) (*Enricher, error) {
	var config *rest.Config
	var err error

	if kubeconfig == "" {
		// Running inside a cluster
		config, err = rest.InClusterConfig()
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if err != nil {
		return nil, fmt.Errorf("k8s config: %w", err)
	}

	k8s, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("k8s client: %w", err)
	}

	e := &Enricher{
		k8s:            k8s,
		cgroupToPod:    make(map[uint64]*PodInfo),
		containerToPod: make(map[string]*PodInfo),
	}

	// Initial sync
	if err := e.syncPods(); err != nil {
		return nil, fmt.Errorf("initial pod sync: %w", err)
	}

	return e, nil
}

// StartSync begins background pod list refresh.
func (e *Enricher) StartSync(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = e.syncPods()
		}
	}
}

// Resolve returns PodInfo for a given cgroup ID.
// Returns nil if the cgroup is not associated with a K8s pod.
func (e *Enricher) Resolve(cgroupID uint64) *PodInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cgroupToPod[cgroupID]
}

// syncPods fetches all pods from K8s API and rebuilds the cgroup map.
func (e *Enricher) syncPods() error {
	pods, err := e.k8s.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}

	newCgroupMap := make(map[uint64]*PodInfo)
	newContainerMap := make(map[string]*PodInfo)

	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			containerID := sanitizeContainerID(cs.ContainerID)
			if containerID == "" {
				continue
			}

			info := &PodInfo{
				PodName:      pod.Name,
				Namespace:    pod.Namespace,
				NodeName:     pod.Spec.NodeName,
				TeamLabel:    labelOrDefault(pod.Labels, "app.kubernetes.io/team", "unknown"),
				ServiceLabel: labelOrDefault(pod.Labels, "app.kubernetes.io/name", pod.Name),
				FeatureLabel: labelOrDefault(pod.Labels, "feature", ""),
				CostCenter:   labelOrDefault(pod.Labels, "billing/cost-center", ""),
				Environment:  labelOrDefault(pod.Labels, "environment", "production"),
				ContainerID:  containerID,
			}

			newContainerMap[containerID] = info

			// Resolve cgroup ID from containerID via /sys/fs/cgroup
			if cgroupID, err := resolveCgroupID(containerID); err == nil {
				info.CgroupID = cgroupID
				newCgroupMap[cgroupID] = info
			}
		}
	}

	e.mu.Lock()
	e.cgroupToPod = newCgroupMap
	e.containerToPod = newContainerMap
	e.mu.Unlock()

	return nil
}

// resolveCgroupID finds the cgroup ID for a container by reading
// /sys/fs/cgroup hierarchy and matching the container ID in the path.
func resolveCgroupID(containerID string) (uint64, error) {
	// Walk /sys/fs/cgroup/kubepods to find the container's cgroup
	cgroupRoot := "/sys/fs/cgroup/kubepods"
	var cgroupID uint64

	err := filepath.WalkDir(cgroupRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if !strings.Contains(path, containerID) {
			return nil
		}
		// Read cgroup.id file (cgroup v2)
		idFile := filepath.Join(path, "cgroup.id")
		f, err := os.Open(idFile)
		if err != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		if scanner.Scan() {
			_, err = fmt.Sscanf(scanner.Text(), "%d", &cgroupID)
		}
		return filepath.SkipAll
	})

	if err != nil || cgroupID == 0 {
		return 0, fmt.Errorf("cgroup not found for container %s", containerID)
	}
	return cgroupID, nil
}

// sanitizeContainerID strips the runtime prefix (e.g. "docker://", "containerd://")
func sanitizeContainerID(id string) string {
	for _, prefix := range []string{"docker://", "containerd://", "cri-o://"} {
		id = strings.TrimPrefix(id, prefix)
	}
	// Use first 12 chars (short ID)
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func labelOrDefault(labels map[string]string, key, def string) string {
	if v, ok := labels[key]; ok && v != "" {
		return v
	}
	return def
}
