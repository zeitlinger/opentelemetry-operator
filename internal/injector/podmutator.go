// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package injector

import (
	"context"
	"sort"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/open-telemetry/opentelemetry-operator/apis/v2alpha1"
	"github.com/open-telemetry/opentelemetry-operator/internal/webhook/podmutation"
)

var _ podmutation.PodMutator = (*injectorPodMutator)(nil)

type injectorPodMutator struct {
	Logger logr.Logger
	Client client.Client
}

// NewMutator creates a PodMutator that injects the composite SDK via LD_PRELOAD.
func NewMutator(logger logr.Logger, client client.Client) podmutation.PodMutator {
	return &injectorPodMutator{
		Logger: logger,
		Client: client,
	}
}

func (pm *injectorPodMutator) Mutate(ctx context.Context, ns corev1.Namespace, pod corev1.Pod) (corev1.Pod, error) {
	logger := pm.Logger.WithValues("namespace", ns.Name)
	if pod.Name != "" {
		logger = logger.WithValues("name", pod.Name)
	} else if pod.GenerateName != "" {
		logger = logger.WithValues("generateName", pod.GenerateName)
	}

	inst := pm.selectInstrumentation(ctx, ns, pod)
	if inst == nil {
		logger.V(1).Info("no matching v2alpha1 Instrumentation CR for this pod")
		return pod, nil
	}

	if isAlreadyInjected(pod) {
		logger.Info("Skipping injector injection - already injected")
		return pod, nil
	}

	return injectPod(inst, pod, ns.Name)
}

// selectInstrumentation lists all Instrumentation CRs and returns the highest-priority
// one that has at least one rule matching this pod. Returns nil if nothing matches.
func (pm *injectorPodMutator) selectInstrumentation(ctx context.Context, ns corev1.Namespace, pod corev1.Pod) *v2alpha1.Instrumentation {
	var list v2alpha1.InstrumentationList
	if err := pm.Client.List(ctx, &list); err != nil {
		pm.Logger.Error(err, "failed to list v2alpha1 Instrumentation CRs")
		return nil
	}

	// Sort by priority descending, then creation timestamp ascending (oldest wins ties).
	sort.Slice(list.Items, func(i, j int) bool {
		if list.Items[i].Spec.Priority != list.Items[j].Spec.Priority {
			return list.Items[i].Spec.Priority > list.Items[j].Spec.Priority
		}
		return list.Items[i].CreationTimestamp.Before(&list.Items[j].CreationTimestamp)
	})

	for i := range list.Items {
		inst := &list.Items[i]
		if hasMatchingRule(inst, ns.Name, pod) {
			return inst
		}
	}
	return nil
}

// hasMatchingRule returns true if any rule in the CR matches the pod's namespace
// and labels (container-level matching is done at injection time).
func hasMatchingRule(inst *v2alpha1.Instrumentation, namespace string, pod corev1.Pod) bool {
	for _, rule := range inst.Spec.Rules {
		if matchesNamespace(rule.Selector, namespace) && matchesPodLabels(rule.Selector, pod.Labels) {
			return true
		}
	}
	return false
}

