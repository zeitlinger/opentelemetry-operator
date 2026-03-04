// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package injector

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/open-telemetry/opentelemetry-operator/apis/v2alpha1"
)

func newReconciler(objs ...client.Object) (*InstrumentationReconciler, client.Client) {
	s := newScheme()
	_ = corev1.AddToScheme(s)

	cli := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		Build()

	r := NewInstrumentationReconciler(cli, s, logr.Discard())
	return r, cli
}

func TestReconcile_CreatesConfigMaps(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod"}}
	inst := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "my-inst"},
		Spec: v2alpha1.InstrumentationSpec{
			Rules: []v2alpha1.Rule{
				{
					Name: "with-config",
					Selector: v2alpha1.RuleSelector{
						Namespaces: []string{"prod"},
					},
					Config: v2alpha1.RuleConfig{
						DeclarativeConfig: &v2alpha1.DeclarativeConfig{
							Object: map[string]any{
								"file_format": "1.0",
								"tracer_provider": map[string]any{
									"processors": []any{},
								},
							},
						},
					},
				},
			},
		},
	}

	r, cli := newReconciler(ns, inst)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(inst),
	})
	require.NoError(t, err)

	// Verify ConfigMap was created.
	cmName := ConfigMapName("my-inst", "with-config")
	var cm corev1.ConfigMap
	err = cli.Get(context.Background(), client.ObjectKey{Namespace: "prod", Name: cmName}, &cm)
	require.NoError(t, err)

	assert.Equal(t, labelManagedByValue, cm.Labels[labelManagedBy])
	assert.Equal(t, "my-inst", cm.Labels[labelInstrumentation])
	assert.Equal(t, "with-config", cm.Labels[labelRule])
	assert.Contains(t, cm.Data[configMapDataKey], "file_format")

	// Verify finalizer was added.
	var updated v2alpha1.Instrumentation
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(inst), &updated)
	require.NoError(t, err)
	assert.Contains(t, updated.Finalizers, finalizerName)
}

func TestReconcile_CatchAllCreatesInAllNamespaces(t *testing.T) {
	ns1 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns1"}}
	ns2 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns2"}}
	inst := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "global"},
		Spec: v2alpha1.InstrumentationSpec{
			Rules: []v2alpha1.Rule{
				{
					Name: "catch-all",
					Config: v2alpha1.RuleConfig{
						DeclarativeConfig: &v2alpha1.DeclarativeConfig{
							Object: map[string]any{"file_format": "1.0"},
						},
					},
				},
			},
		},
	}

	r, cli := newReconciler(ns1, ns2, inst)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(inst),
	})
	require.NoError(t, err)

	cmName := ConfigMapName("global", "catch-all")
	for _, ns := range []string{"ns1", "ns2"} {
		var cm corev1.ConfigMap
		err = cli.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: cmName}, &cm)
		require.NoError(t, err, "expected ConfigMap in namespace %s", ns)
	}
}

func TestReconcile_PrunesStaleConfigMaps(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	inst := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "my-inst"},
		Spec: v2alpha1.InstrumentationSpec{
			Rules: []v2alpha1.Rule{
				{
					Name: "rule-a",
					Selector: v2alpha1.RuleSelector{
						Namespaces: []string{"default"},
					},
					Config: v2alpha1.RuleConfig{
						DeclarativeConfig: &v2alpha1.DeclarativeConfig{
							Object: map[string]any{"file_format": "1.0"},
						},
					},
				},
			},
		},
	}

	// Pre-existing stale ConfigMap from a removed rule.
	staleCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName("my-inst", "old-rule"),
			Namespace: "default",
			Labels: map[string]string{
				labelManagedBy:       labelManagedByValue,
				labelInstrumentation: "my-inst",
				labelRule:            "old-rule",
			},
		},
	}

	r, cli := newReconciler(ns, inst, staleCM)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(inst),
	})
	require.NoError(t, err)

	// Stale ConfigMap should be gone.
	var cm corev1.ConfigMap
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(staleCM), &cm)
	assert.True(t, err != nil, "stale ConfigMap should have been deleted")

	// Desired ConfigMap should exist.
	err = cli.Get(context.Background(), client.ObjectKey{
		Namespace: "default",
		Name:      ConfigMapName("my-inst", "rule-a"),
	}, &cm)
	require.NoError(t, err)
}

func TestReconcile_NoDeclarativeConfig_NoFinalizer(t *testing.T) {
	inst := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "simple"},
		Spec: v2alpha1.InstrumentationSpec{
			Rules: []v2alpha1.Rule{
				{Name: "catch-all"},
			},
		},
	}

	r, cli := newReconciler(inst)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(inst),
	})
	require.NoError(t, err)

	var updated v2alpha1.Instrumentation
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(inst), &updated)
	require.NoError(t, err)
	assert.NotContains(t, updated.Finalizers, finalizerName)
}

func TestReconcile_UpdatesExistingConfigMap(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	inst := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "my-inst"},
		Spec: v2alpha1.InstrumentationSpec{
			Rules: []v2alpha1.Rule{
				{
					Name: "rule-a",
					Selector: v2alpha1.RuleSelector{
						Namespaces: []string{"default"},
					},
					Config: v2alpha1.RuleConfig{
						DeclarativeConfig: &v2alpha1.DeclarativeConfig{
							Object: map[string]any{"file_format": "2.0"},
						},
					},
				},
			},
		},
	}

	// Pre-existing ConfigMap with old data.
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName("my-inst", "rule-a"),
			Namespace: "default",
			Labels: map[string]string{
				labelManagedBy:       labelManagedByValue,
				labelInstrumentation: "my-inst",
				labelRule:            "rule-a",
			},
		},
		Data: map[string]string{configMapDataKey: "old-data"},
	}

	r, cli := newReconciler(ns, inst, existing)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(inst),
	})
	require.NoError(t, err)

	var cm corev1.ConfigMap
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(existing), &cm)
	require.NoError(t, err)
	assert.Contains(t, cm.Data[configMapDataKey], "2.0")
	assert.NotContains(t, cm.Data[configMapDataKey], "old-data")
}

func TestConfigMapName(t *testing.T) {
	assert.Equal(t, "otel-injector-my-inst-my-rule", ConfigMapName("my-inst", "my-rule"))
}

func TestConfigMapName_Truncation(t *testing.T) {
	longName := make([]byte, 300)
	for i := range longName {
		longName[i] = 'a'
	}
	result := ConfigMapName(string(longName), "rule")
	assert.LessOrEqual(t, len(result), 253)
}

func TestEnqueueAllInstrumentations(t *testing.T) {
	inst1 := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "inst1"},
	}
	inst2 := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "inst2"},
	}

	r, _ := newReconciler(inst1, inst2)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "new-ns"}}
	requests := r.enqueueAllInstrumentations(context.Background(), ns)

	assert.Len(t, requests, 2)
	names := map[string]bool{}
	for _, req := range requests {
		names[req.Name] = true
	}
	assert.True(t, names["inst1"])
	assert.True(t, names["inst2"])
}
