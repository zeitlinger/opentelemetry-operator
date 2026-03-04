// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package injector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/open-telemetry/opentelemetry-operator/apis/v2alpha1"
)

func TestInjectPod_Basic(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{
				Image: "ghcr.io/example/composite-sdk:latest",
			},
			Rules: []v2alpha1.Rule{
				{
					Name: "catch-all",
					Config: v2alpha1.RuleConfig{
						Env: []corev1.EnvVar{
							{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://otel-collector:4318"},
							{Name: "OTEL_TRACES_SAMPLER", Value: "parentbased_traceidratio"},
							{Name: "OTEL_TRACES_SAMPLER_ARG", Value: "0.25"},
							{Name: "OTEL_PROPAGATORS", Value: "tracecontext,baggage"},
						},
					},
				},
			},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "myapp:latest"},
			},
		},
	}

	result := mustInjectPod(t, inst, pod, "default")

	// Init container added
	require.Len(t, result.Spec.InitContainers, 1)
	assert.Equal(t, initContainerName, result.Spec.InitContainers[0].Name)
	assert.Equal(t, "ghcr.io/example/composite-sdk:latest", result.Spec.InitContainers[0].Image)
	assert.Equal(t, []string{"cp", "-r", "/autoinstrumentation/.", mountPath}, result.Spec.InitContainers[0].Command)

	// Volume added
	require.Len(t, result.Spec.Volumes, 1)
	assert.Equal(t, volumeName, result.Spec.Volumes[0].Name)
	assert.NotNil(t, result.Spec.Volumes[0].EmptyDir)

	// Container has volume mount
	require.Len(t, result.Spec.Containers[0].VolumeMounts, 1)
	assert.Equal(t, mountPath, result.Spec.Containers[0].VolumeMounts[0].MountPath)

	// Check env vars
	envMap := envToMap(result.Spec.Containers[0].Env)
	assert.Equal(t, ldPreloadPath, envMap[envLDPreload])
	assert.Equal(t, configFilePath, envMap[envInjectorConfigFile])
	assert.Equal(t, "http/protobuf", envMap[envOTLPProtocol])
	assert.Equal(t, "http://otel-collector:4318", envMap["OTEL_EXPORTER_OTLP_ENDPOINT"])
	assert.Equal(t, "parentbased_traceidratio", envMap["OTEL_TRACES_SAMPLER"])
	assert.Equal(t, "0.25", envMap["OTEL_TRACES_SAMPLER_ARG"])
	assert.Equal(t, "tracecontext,baggage", envMap["OTEL_PROPAGATORS"])
	assert.Equal(t, "app", envMap[envInjectorK8sContainerName])
	assert.Equal(t, "test-pod", envMap[envInjectorServiceName])
	assert.Equal(t, "default", envMap[envInjectorServiceNamespace])

	// Downward API refs
	for _, name := range []string{envInjectorK8sNamespace, envInjectorK8sPodName, envInjectorK8sPodUID} {
		found := findEnv(result.Spec.Containers[0].Env, name)
		require.NotNil(t, found, "expected env var %s", name)
		assert.NotNil(t, found.ValueFrom, "expected ValueFrom for %s", name)
	}
}

func TestInjectPod_RuleMatchesByNamespace(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Name: "prod-only",
					Selector: v2alpha1.RuleSelector{
						Namespaces: []string{"production"},
					},
					Config: v2alpha1.RuleConfig{
						Env: []corev1.EnvVar{{Name: "ENV", Value: "prod"}},
					},
				},
			},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	// Should not match — pod is in "default", rule targets "production"
	result := mustInjectPod(t, inst, pod, "default")
	assert.Empty(t, result.Spec.Containers[0].Env)

	// Should match
	result = mustInjectPod(t, inst, pod, "production")
	envMap := envToMap(result.Spec.Containers[0].Env)
	assert.Equal(t, "prod", envMap["ENV"])
	assert.Equal(t, ldPreloadPath, envMap[envLDPreload])
}

func TestInjectPod_RuleMatchesByPodLabels(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Selector: v2alpha1.RuleSelector{
						PodLabels: map[string]string{"app": "frontend"},
					},
					Config: v2alpha1.RuleConfig{
						Env: []corev1.EnvVar{{Name: "ROLE", Value: "frontend"}},
					},
				},
			},
		},
	}

	// No labels — should not match
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}
	result := mustInjectPod(t, inst, pod, "default")
	assert.Empty(t, result.Spec.Containers[0].Env)

	// Matching labels
	pod.Labels = map[string]string{"app": "frontend", "version": "v1"}
	result = mustInjectPod(t, inst, pod, "default")
	envMap := envToMap(result.Spec.Containers[0].Env)
	assert.Equal(t, "frontend", envMap["ROLE"])
}

func TestInjectPod_RuleMatchesByContainerName(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Selector: v2alpha1.RuleSelector{
						ContainerNames: []string{"sidecar"},
					},
					Config: v2alpha1.RuleConfig{
						Env: []corev1.EnvVar{{Name: "TARGET", Value: "sidecar"}},
					},
				},
			},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app"},
				{Name: "sidecar"},
			},
		},
	}

	result := mustInjectPod(t, inst, pod, "default")

	// "app" should NOT be injected (no matching rule)
	assert.Empty(t, result.Spec.Containers[0].Env)
	assert.Empty(t, result.Spec.Containers[0].VolumeMounts)

	// "sidecar" should be injected
	envMap := envToMap(result.Spec.Containers[1].Env)
	assert.Equal(t, "sidecar", envMap["TARGET"])
	assert.Equal(t, ldPreloadPath, envMap[envLDPreload])
	assert.Len(t, result.Spec.Containers[1].VolumeMounts, 1)
}

func TestInjectPod_DisabledRule(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Name: "opt-out-sidecar",
					Selector: v2alpha1.RuleSelector{
						ContainerNames: []string{"sidecar"},
					},
					Config: v2alpha1.RuleConfig{Disabled: true},
				},
				{
					Name: "catch-all",
				},
			},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app"},
				{Name: "sidecar"},
			},
		},
	}

	result := mustInjectPod(t, inst, pod, "default")

	// "app" matches catch-all → injected
	envMap := envToMap(result.Spec.Containers[0].Env)
	assert.Equal(t, ldPreloadPath, envMap[envLDPreload])

	// "sidecar" matches disabled rule → NOT injected
	assert.Empty(t, result.Spec.Containers[1].Env)
	assert.Empty(t, result.Spec.Containers[1].VolumeMounts)
}

func TestInjectPod_FirstMatchWins(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Name: "specific",
					Selector: v2alpha1.RuleSelector{
						PodLabels: map[string]string{"app": "api"},
					},
					Config: v2alpha1.RuleConfig{
						Env: []corev1.EnvVar{{Name: "MATCHED", Value: "specific"}},
					},
				},
				{
					Name: "catch-all",
					Config: v2alpha1.RuleConfig{
						Env: []corev1.EnvVar{{Name: "MATCHED", Value: "catch-all"}},
					},
				},
			},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test",
			Labels: map[string]string{"app": "api"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	result := mustInjectPod(t, inst, pod, "default")
	envMap := envToMap(result.Spec.Containers[0].Env)
	assert.Equal(t, "specific", envMap["MATCHED"])
}

func TestInjectPod_SkipsContainerWithExistingLDPreload(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules:    []v2alpha1.Rule{{Name: "catch-all"}},
		},
	}

	pod := corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "already-instrumented",
					Image: "app:latest",
					Env:   []corev1.EnvVar{{Name: envLDPreload, Value: "/other/lib.so"}},
				},
				{
					Name:  "not-instrumented",
					Image: "app2:latest",
				},
			},
		},
	}

	result := mustInjectPod(t, inst, pod, "default")

	// First container should not get extra env vars
	assert.Len(t, result.Spec.Containers[0].Env, 1)
	assert.Empty(t, result.Spec.Containers[0].VolumeMounts)

	// Second container should be injected
	envMap := envToMap(result.Spec.Containers[1].Env)
	assert.Equal(t, ldPreloadPath, envMap[envLDPreload])
	assert.Len(t, result.Spec.Containers[1].VolumeMounts, 1)
}

func TestInjectPod_NoMatchingRules(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Selector: v2alpha1.RuleSelector{
						Namespaces: []string{"production"},
					},
				},
			},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	result := mustInjectPod(t, inst, pod, "default")
	// No matching rule → container should not be touched
	assert.Empty(t, result.Spec.Containers[0].Env)
	assert.Empty(t, result.Spec.Containers[0].VolumeMounts)
}

func TestIsAlreadyInjected_InitContainer(t *testing.T) {
	pod := corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: initContainerName},
			},
		},
	}
	assert.True(t, isAlreadyInjected(pod))
}

func TestIsAlreadyInjected_LDPreload(t *testing.T) {
	pod := corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Env:  []corev1.EnvVar{{Name: envLDPreload, Value: ldPreloadPath}},
				},
			},
		},
	}
	assert.True(t, isAlreadyInjected(pod))
}

func TestIsAlreadyInjected_Clean(t *testing.T) {
	pod := corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app"},
			},
		},
	}
	assert.False(t, isAlreadyInjected(pod))
}

func TestDeriveServiceName_Deployment(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "myapp-7b9f5d4c8f"},
			},
		},
	}
	assert.Equal(t, "myapp", deriveServiceName(pod))
}

func TestDeriveServiceName_ReplicaSetWithDeploymentHash(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "my-deployment-abc123"},
			},
		},
	}
	assert.Equal(t, "my-deployment", deriveServiceName(pod))
}

func TestDeriveServiceName_StatefulSet(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "StatefulSet", Name: "my-statefulset"},
			},
		},
	}
	assert.Equal(t, "my-statefulset", deriveServiceName(pod))
}

func TestDeriveServiceName_FallbackToPodName(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "standalone-pod",
		},
	}
	assert.Equal(t, "standalone-pod", deriveServiceName(pod))
}

func TestDeriveServiceName_FallbackToGenerateName(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "job-runner-",
		},
	}
	assert.Equal(t, "job-runner-", deriveServiceName(pod))
}

func TestInjectPod_EmptyRuleConfig(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules:    []v2alpha1.Rule{{Name: "catch-all"}},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	result := mustInjectPod(t, inst, pod, "default")
	envMap := envToMap(result.Spec.Containers[0].Env)

	// Core env vars should always be set
	assert.Equal(t, ldPreloadPath, envMap[envLDPreload])
	assert.Equal(t, configFilePath, envMap[envInjectorConfigFile])
	assert.Equal(t, "http/protobuf", envMap[envOTLPProtocol])
}

func TestInjectPod_OTLPProtocolDefault(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules:    []v2alpha1.Rule{{Name: "catch-all"}},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	// Default: OTLP protocol is injected as http/protobuf
	result := mustInjectPod(t, inst, pod, "default")
	envMap := envToMap(result.Spec.Containers[0].Env)
	assert.Equal(t, "http/protobuf", envMap[envOTLPProtocol])
}

func TestInjectPod_OTLPProtocolUserOverride(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Name: "grpc-rule",
					Config: v2alpha1.RuleConfig{
						Env: []corev1.EnvVar{
							{Name: envOTLPProtocol, Value: "grpc"},
						},
					},
				},
			},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	result := mustInjectPod(t, inst, pod, "default")

	// User's value must win and there must be no duplicate.
	envs := result.Spec.Containers[0].Env
	assert.Equal(t, 1, countEnv(envs, envOTLPProtocol), "expected exactly one %s", envOTLPProtocol)
	assert.Equal(t, "grpc", findEnv(envs, envOTLPProtocol).Value)
}

func TestInjectPod_RejectsOtelInjectorEnvVars(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Name: "sneaky-rule",
					Config: v2alpha1.RuleConfig{
						Env: []corev1.EnvVar{
							{Name: "OTEL_INJECTOR_SERVICE_NAME", Value: "hacked"},
							{Name: "OTEL_TRACES_SAMPLER", Value: "always_on"},
						},
					},
				},
			},
		},
	}

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	_, err := injectPod(inst, pod, "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OTEL_INJECTOR_SERVICE_NAME")
	assert.Contains(t, err.Error(), "sneaky-rule")
}

// helpers

func envToMap(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string)
	for _, e := range envs {
		if e.ValueFrom == nil {
			m[e.Name] = e.Value
		}
	}
	return m
}

func findEnv(envs []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == name {
			return &envs[i]
		}
	}
	return nil
}

func countEnv(envs []corev1.EnvVar, name string) int {
	n := 0
	for _, e := range envs {
		if e.Name == name {
			n++
		}
	}
	return n
}

func mustInjectPod(t *testing.T, inst *v2alpha1.Instrumentation, pod corev1.Pod, namespace string) corev1.Pod {
	t.Helper()
	result, err := injectPod(inst, pod, namespace)
	require.NoError(t, err)
	return result
}
