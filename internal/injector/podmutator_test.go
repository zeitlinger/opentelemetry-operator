// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package injector

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/open-telemetry/opentelemetry-operator/apis/v2alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v2alpha1.AddToScheme(s)
	return s
}

func TestSelectInstrumentation_HigherPriorityWins(t *testing.T) {
	lowPriority := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "low",
			CreationTimestamp:  metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
		Spec: v2alpha1.InstrumentationSpec{
			Priority: 10,
			Injector: v2alpha1.InjectorSpec{Image: "sdk:low"},
			Rules:    []v2alpha1.Rule{{Name: "catch-all"}},
		},
	}
	highPriority := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "high",
			CreationTimestamp:  metav1.NewTime(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)),
		},
		Spec: v2alpha1.InstrumentationSpec{
			Priority: 100,
			Injector: v2alpha1.InjectorSpec{Image: "sdk:high"},
			Rules:    []v2alpha1.Rule{{Name: "catch-all"}},
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(lowPriority, highPriority).
		Build()

	pm := &injectorPodMutator{Logger: logr.Discard(), Client: cli}
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}

	inst := pm.selectInstrumentation(context.Background(), ns, pod)
	require.NotNil(t, inst)
	assert.Equal(t, "high", inst.Name)
}

func TestSelectInstrumentation_OldestWinsTie(t *testing.T) {
	older := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "older",
			CreationTimestamp:  metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
		Spec: v2alpha1.InstrumentationSpec{
			Priority: 50,
			Injector: v2alpha1.InjectorSpec{Image: "sdk:older"},
			Rules:    []v2alpha1.Rule{{Name: "catch-all"}},
		},
	}
	newer := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "newer",
			CreationTimestamp:  metav1.NewTime(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)),
		},
		Spec: v2alpha1.InstrumentationSpec{
			Priority: 50,
			Injector: v2alpha1.InjectorSpec{Image: "sdk:newer"},
			Rules:    []v2alpha1.Rule{{Name: "catch-all"}},
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(newer, older). // insert out of order to verify sorting
		Build()

	pm := &injectorPodMutator{Logger: logr.Discard(), Client: cli}
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}

	inst := pm.selectInstrumentation(context.Background(), ns, pod)
	require.NotNil(t, inst)
	assert.Equal(t, "older", inst.Name)
}

func TestSelectInstrumentation_NoMatchReturnsNil(t *testing.T) {
	cr := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-only"},
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Name: "prod",
					Selector: v2alpha1.RuleSelector{
						Namespaces: []string{"production"},
					},
				},
			},
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(cr).
		Build()

	pm := &injectorPodMutator{Logger: logr.Discard(), Client: cli}
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}

	inst := pm.selectInstrumentation(context.Background(), ns, pod)
	assert.Nil(t, inst)
}

func TestSelectInstrumentation_MatchesByPodLabels(t *testing.T) {
	cr := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "label-match"},
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules: []v2alpha1.Rule{
				{
					Name: "frontend",
					Selector: v2alpha1.RuleSelector{
						PodLabels: map[string]string{"app": "frontend"},
					},
				},
			},
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(cr).
		Build()

	pm := &injectorPodMutator{Logger: logr.Discard(), Client: cli}
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}

	// No labels — should not match
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	assert.Nil(t, pm.selectInstrumentation(context.Background(), ns, pod))

	// Matching labels
	pod.Labels = map[string]string{"app": "frontend"}
	inst := pm.selectInstrumentation(context.Background(), ns, pod)
	require.NotNil(t, inst)
	assert.Equal(t, "label-match", inst.Name)
}

func TestMutate_AlreadyInjectedSkips(t *testing.T) {
	cr := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cr"},
		Spec: v2alpha1.InstrumentationSpec{
			Injector: v2alpha1.InjectorSpec{Image: "sdk:latest"},
			Rules:    []v2alpha1.Rule{{Name: "catch-all"}},
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(cr).
		Build()

	pm := &injectorPodMutator{Logger: logr.Discard(), Client: cli}
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: initContainerName}},
			Containers:     []corev1.Container{{Name: "app"}},
		},
	}

	result, err := pm.Mutate(context.Background(), ns, pod)
	require.NoError(t, err)
	// Should not add a second init container
	assert.Len(t, result.Spec.InitContainers, 1)
	assert.Empty(t, result.Spec.Containers[0].Env)
}
