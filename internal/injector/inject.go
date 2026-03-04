// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package injector

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/open-telemetry/opentelemetry-operator/apis/v2alpha1"
)

const (
	initContainerName = "otel-injector-init"
	volumeName        = "otel-injector"
	mountPath         = "/otel"
	ldPreloadPath     = "/otel/libotelinject.so"
	configFilePath    = "/otel/injector/otelinject.conf"

	envLDPreload                = "LD_PRELOAD"
	envInjectorConfigFile       = "OTEL_INJECTOR_CONFIG_FILE"
	envOTLPProtocol             = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envInjectorK8sNamespace     = "OTEL_INJECTOR_K8S_NAMESPACE_NAME"
	envInjectorK8sPodName       = "OTEL_INJECTOR_K8S_POD_NAME"
	envInjectorK8sPodUID        = "OTEL_INJECTOR_K8S_POD_UID"
	envInjectorK8sContainerName = "OTEL_INJECTOR_K8S_CONTAINER_NAME"
	envInjectorServiceName      = "OTEL_INJECTOR_SERVICE_NAME"
	envInjectorServiceNamespace = "OTEL_INJECTOR_SERVICE_NAMESPACE"

	envNodeIP   = "OTEL_NODE_IP"
	envPodIP    = "OTEL_POD_IP"
	envNodeName = "OTEL_NODE_NAME"

	envInjectorResourceAttributes = "OTEL_INJECTOR_RESOURCE_ATTRIBUTES"
)

func isAlreadyInjected(pod corev1.Pod) bool {
	for _, c := range pod.Spec.InitContainers {
		if c.Name == initContainerName {
			return true
		}
	}
	for _, c := range pod.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == envLDPreload {
				return true
			}
		}
	}
	return false
}

func injectPod(inst *v2alpha1.Instrumentation, pod corev1.Pod, namespace string) (corev1.Pod, error) {
	// Validate all rules up front before mutating the pod.
	for _, rule := range inst.Spec.Rules {
		if err := validateRuleEnv(rule); err != nil {
			return pod, err
		}
	}

	// Add volume
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})

	// Add init container
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name:    initContainerName,
		Image:   inst.Spec.Injector.Image,
		Command: []string{"cp", "-r", "/autoinstrumentation/.", mountPath},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volumeName,
			MountPath: mountPath,
		}},
	})

	serviceName := deriveServiceName(pod)

	// Inject env vars into each app container based on matching rules.
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]

		// Skip containers that already have LD_PRELOAD
		if hasEnv(c.Env, envLDPreload) {
			continue
		}

		rule := matchRule(inst.Spec.Rules, namespace, pod.Labels, c.Name)
		if rule == nil {
			continue
		}

		// Disabled rule = explicit opt-out for this container.
		if rule.Config.Disabled {
			continue
		}

		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: mountPath,
		})

		envVars := buildEnvVars(rule, c.Name, serviceName, namespace, pod.OwnerReferences)
		c.Env = append(c.Env, envVars...)
	}

	return pod, nil
}

// matchRule returns the first matching rule for the given container, or nil.
func matchRule(rules []v2alpha1.Rule, namespace string, podLabels map[string]string, containerName string) *v2alpha1.Rule {
	for i := range rules {
		r := &rules[i]
		if matchesNamespace(r.Selector, namespace) &&
			matchesPodLabels(r.Selector, podLabels) &&
			matchesContainerName(r.Selector, containerName) {
			return r
		}
	}
	return nil
}

// matchesNamespace returns true if the selector's namespace list is empty or contains the given namespace.
func matchesNamespace(sel v2alpha1.RuleSelector, namespace string) bool {
	if len(sel.Namespaces) == 0 {
		return true
	}
	for _, ns := range sel.Namespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

// matchesPodLabels returns true if the pod has all labels specified in the selector (AND semantics).
func matchesPodLabels(sel v2alpha1.RuleSelector, podLabels map[string]string) bool {
	for k, v := range sel.PodLabels {
		if podLabels[k] != v {
			return false
		}
	}
	return true
}

// matchesContainerName returns true if the selector's container list is empty or contains the name.
func matchesContainerName(sel v2alpha1.RuleSelector, name string) bool {
	if len(sel.ContainerNames) == 0 {
		return true
	}
	for _, cn := range sel.ContainerNames {
		if cn == name {
			return true
		}
	}
	return false
}

// validateRuleEnv returns an error if any env var in the rule uses the reserved OTEL_INJECTOR_ prefix.
func validateRuleEnv(rule v2alpha1.Rule) error {
	for _, e := range rule.Config.Env {
		if strings.HasPrefix(e.Name, "OTEL_INJECTOR_") {
			return fmt.Errorf("rule %q: env var %q uses reserved OTEL_INJECTOR_ prefix", rule.Name, e.Name)
		}
	}
	return nil
}

// buildEnvVars constructs the env vars injected into each instrumented container.
//
// The operator sets OTEL_INJECTOR_* env vars for K8s metadata and service identity.
// The injector binary (LD_PRELOAD) merges these into OTEL_RESOURCE_ATTRIBUTES at
// runtime. See opentelemetry-injector/src/resource_attributes.zig for details.
//
// Resource attribute precedence (highest first):
//   - OTEL_RESOURCE_ATTRIBUTES: user-set keys are always preserved
//   - OTEL_INJECTOR_RESOURCE_ATTRIBUTES: adds keys not already present
//   - OTEL_INJECTOR_* individual vars: adds keys not already present
//
// Service name precedence (highest first):
//   - OTEL_SERVICE_NAME: SDK reads it directly, ignores resource attrs
//   - OTEL_RESOURCE_ATTRIBUTES with service.name: injector preserves it
//   - OTEL_INJECTOR_SERVICE_NAME: operator-derived fallback from owner refs
func buildEnvVars(rule *v2alpha1.Rule, containerName, serviceName, namespace string, ownerRefs []metav1.OwnerReference) []corev1.EnvVar {
	// User env vars go first so they win over operator defaults (K8s uses first occurrence).
	envs := append([]corev1.EnvVar{}, rule.Config.Env...)

	// Operator defaults — only added if the user hasn't set them.
	appendIfNotSet(&envs, corev1.EnvVar{Name: envOTLPProtocol, Value: "http/protobuf"})
	appendIfNotSet(&envs, corev1.EnvVar{
		Name: envNodeIP,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"},
		},
	})
	appendIfNotSet(&envs, corev1.EnvVar{
		Name: envPodIP,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
		},
	})

	// OTEL_INJECTOR_* vars are always set (users can't set these — blocked by validation).
	envs = append(envs,
		corev1.EnvVar{Name: envLDPreload, Value: ldPreloadPath},
		corev1.EnvVar{Name: envInjectorConfigFile, Value: configFilePath},
		corev1.EnvVar{
			Name: envInjectorK8sNamespace,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
		corev1.EnvVar{
			Name: envInjectorK8sPodName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
		corev1.EnvVar{
			Name: envInjectorK8sPodUID,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"},
			},
		},
		corev1.EnvVar{
			Name: envNodeName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
			},
		},
		corev1.EnvVar{Name: envInjectorK8sContainerName, Value: containerName},
		// Container name is the last fallback per semconv spec.
		corev1.EnvVar{Name: envInjectorServiceName, Value: serviceNameWithFallback(serviceName, containerName)},
		corev1.EnvVar{Name: envInjectorServiceNamespace, Value: namespace},
		corev1.EnvVar{Name: envInjectorResourceAttributes, Value: buildInjectorResourceAttrs(containerName, ownerRefs)},
	)

	return envs
}

// buildInjectorResourceAttrs constructs the OTEL_INJECTOR_RESOURCE_ATTRIBUTES value.
// Uses $(...) references for values only known at runtime (resolved by K8s variable expansion).
func buildInjectorResourceAttrs(containerName string, ownerRefs []metav1.OwnerReference) string {
	var attrs []string

	// service.instance.id = namespace.podName.containerName (semconv recommendation for K8s)
	// See https://opentelemetry.io/docs/specs/semconv/non-normative/k8s-attributes/#how-serviceinstanceid-should-be-calculated
	attrs = append(attrs, fmt.Sprintf("service.instance.id=$(%s).$(%s).%s",
		envInjectorK8sNamespace, envInjectorK8sPodName, containerName))

	// k8s.node.name from downward API
	attrs = append(attrs, fmt.Sprintf("k8s.node.name=$(%s)", envNodeName))

	// Owner ref resource attributes (k8s.replicaset.name, k8s.statefulset.name, etc.)
	//
	// For ReplicaSet and Job, we also derive the parent name (Deployment/CronJob) by
	// stripping the trailing hash suffix (e.g. "myapp-abc123" → "myapp"). This avoids
	// an API call to look up the ReplicaSet's owner during webhook admission.
	// v1alpha1 does a real API lookup with retry (sdkInjector.addParentResourceLabels),
	// but that adds latency to pod creation. The heuristic covers the standard case
	// where K8s appends a hyphen + hash to the parent name.
	for _, owner := range ownerRefs {
		if attr := ownerKindToResourceAttribute(owner.Kind); attr != "" {
			attrs = append(attrs, fmt.Sprintf("%s=%s", attr, owner.Name))
		}
		if owner.Kind == "ReplicaSet" {
			if idx := strings.LastIndex(owner.Name, "-"); idx > 0 {
				attrs = append(attrs, fmt.Sprintf("k8s.deployment.name=%s", owner.Name[:idx]))
			}
		}
		if owner.Kind == "Job" {
			if idx := strings.LastIndex(owner.Name, "-"); idx > 0 {
				attrs = append(attrs, fmt.Sprintf("k8s.cronjob.name=%s", owner.Name[:idx]))
			}
		}
	}

	return strings.Join(attrs, ",")
}

func ownerKindToResourceAttribute(kind string) string {
	switch kind {
	case "ReplicaSet":
		return "k8s.replicaset.name"
	case "Deployment":
		return "k8s.deployment.name"
	case "StatefulSet":
		return "k8s.statefulset.name"
	case "DaemonSet":
		return "k8s.daemonset.name"
	case "Job":
		return "k8s.job.name"
	case "CronJob":
		return "k8s.cronjob.name"
	default:
		return ""
	}
}

// deriveServiceName determines service.name following the OTel semconv K8s attribute spec:
// https://opentelemetry.io/docs/specs/semconv/non-normative/k8s-attributes/#how-servicename-should-be-calculated
//
// Precedence (first match wins):
//  1. k8s.deployment.name (derived from ReplicaSet owner by stripping hash suffix)
//  2. k8s.replicaset.name
//  3. k8s.statefulset.name
//  4. k8s.daemonset.name
//  5. k8s.cronjob.name (not reachable — pods don't have CronJob as direct owner)
//  6. k8s.job.name
//  7. k8s.pod.name
//  8. k8s.container.name (applied per-container in buildEnvVars, not here)
func deriveServiceName(pod corev1.Pod) string {
	for _, owner := range pod.OwnerReferences {
		switch owner.Kind {
		case "ReplicaSet":
			name := owner.Name
			if idx := strings.LastIndex(name, "-"); idx > 0 {
				return name[:idx]
			}
			return name
		case "StatefulSet", "DaemonSet", "Job":
			return owner.Name
		}
	}
	return pod.Name
}

func hasEnv(envs []corev1.EnvVar, name string) bool {
	for _, e := range envs {
		if e.Name == name {
			return true
		}
	}
	return false
}

func serviceNameWithFallback(serviceName, containerName string) string {
	if serviceName != "" {
		return serviceName
	}
	return containerName
}

// appendIfNotSet adds the env var only if no env var with the same name is already present.
func appendIfNotSet(envs *[]corev1.EnvVar, env corev1.EnvVar) {
	if !hasEnv(*envs, env.Name) {
		*envs = append(*envs, env)
	}
}
