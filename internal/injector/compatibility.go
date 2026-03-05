// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package injector

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// +kubebuilder:rbac:groups="",resources=nodes,verbs=list

const (
	minKubeMinor       = 32
	minContainerdMajor = 2
	minContainerdMinor = 1
)

// CheckImageVolumeSupport queries the cluster at startup to determine whether
// image volumes are supported. It verifies two things:
//
//  1. The Kubernetes API server is at version 1.32 or later (image volumes were
//     promoted to beta and enabled by default in 1.32).
//  2. Every node runs containerd 2.1 or later (required for OCI image volume
//     mount support).
//
// Returns an empty string if image volumes are supported. Returns a
// human-readable reason string if they are not; this reason is suitable for
// surfacing directly in admission-webhook error messages.
//
// NOTE: This check runs once at startup. Nodes added to the cluster after the
// operator starts will not be rechecked until the operator restarts.
func CheckImageVolumeSupport(ctx context.Context, log logr.Logger, clientset kubernetes.Interface) string {
	// 1. Check Kubernetes server version (>= 1.32).
	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		reason := fmt.Sprintf("failed to query Kubernetes server version: %v", err)
		log.Error(err, "image volume compatibility check failed")
		return reason
	}

	// Strip any trailing "+" appended by some distributions (e.g. kind).
	minorStr := strings.TrimRight(version.Minor, "+")
	minorInt, err := strconv.Atoi(minorStr)
	if err != nil {
		reason := fmt.Sprintf("could not parse Kubernetes server minor version %q", version.Minor)
		log.Info("image volume compatibility check failed", "reason", reason)
		return reason
	}

	if minorInt < minKubeMinor {
		reason := fmt.Sprintf(
			"Kubernetes server version 1.%s is below the minimum required 1.%d for image volume support",
			version.Minor, minKubeMinor,
		)
		log.Info("image volumes not supported", "reason", reason)
		return reason
	}

	// 2. Check all nodes for containerd >= 2.1.
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		reason := fmt.Sprintf("failed to list nodes: %v", err)
		log.Error(err, "image volume compatibility check failed")
		return reason
	}

	for _, node := range nodes.Items {
		runtime := node.Status.NodeInfo.ContainerRuntimeVersion
		isContainerd, major, minor, parseErr := parseContainerdVersion(runtime)
		if parseErr != nil {
			reason := fmt.Sprintf("could not parse container runtime version %q on node %s", runtime, node.Name)
			log.Info("image volumes not supported", "node", node.Name, "reason", reason)
			return reason
		}
		if !isContainerd {
			reason := fmt.Sprintf(
				"node %s uses %q instead of containerd; image volumes require containerd %d.%d+",
				node.Name, runtime, minContainerdMajor, minContainerdMinor,
			)
			log.Info("image volumes not supported", "node", node.Name, "reason", reason)
			return reason
		}
		if major < minContainerdMajor || (major == minContainerdMajor && minor < minContainerdMinor) {
			reason := fmt.Sprintf(
				"node %s has containerd %d.%d which is below the minimum required %d.%d for image volume support",
				node.Name, major, minor, minContainerdMajor, minContainerdMinor,
			)
			log.Info("image volumes not supported", "node", node.Name, "reason", reason)
			return reason
		}
	}

	log.Info("image volume support verified",
		"kubernetes", fmt.Sprintf("1.%s", version.Minor),
		"nodes-checked", len(nodes.Items),
	)
	log.V(1).Info("nodes added after this startup will not be rechecked until the operator restarts")
	return ""
}

// parseContainerdVersion parses a container runtime string like "containerd://2.1.3".
// Returns (isContainerd, major, minor, error). If the string does not start with
// "containerd://", isContainerd is false and the version integers are zero.
func parseContainerdVersion(runtime string) (isContainerd bool, major, minor int, err error) {
	const prefix = "containerd://"
	if !strings.HasPrefix(runtime, prefix) {
		return false, 0, 0, nil
	}
	ver := strings.TrimPrefix(runtime, prefix)
	parts := strings.SplitN(ver, ".", 3)
	if len(parts) < 2 {
		return true, 0, 0, fmt.Errorf("expected major.minor[.patch], got %q", ver)
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return true, 0, 0, fmt.Errorf("parsing major version from %q: %w", ver, err)
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return true, 0, 0, fmt.Errorf("parsing minor version from %q: %w", ver, err)
	}
	return true, major, minor, nil
}
