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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		WithStatusSubresource(&v2alpha1.Instrumentation{}).
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
	kubeSystem := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}
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

	r, cli := newReconciler(ns1, ns2, kubeSystem, inst)

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

	// System namespaces should be excluded from catch-all.
	var cm corev1.ConfigMap
	err = cli.Get(context.Background(), client.ObjectKey{Namespace: "kube-system", Name: cmName}, &cm)
	assert.True(t, apierrors.IsNotFound(err), "should not create ConfigMap in kube-system")
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

	// Different suffixes should produce different names even when truncated.
	result2 := ConfigMapName(string(longName), "other")
	assert.NotEqual(t, result, result2, "truncated names should differ via hash")
}

func TestDeleteAllConfigMaps(t *testing.T) {
	cm1 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName("my-inst", "rule-a"),
			Namespace: "ns1",
			Labels: map[string]string{
				labelManagedBy:       labelManagedByValue,
				labelInstrumentation: "my-inst",
				labelRule:            "rule-a",
			},
		},
	}
	cm2 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName("my-inst", "rule-a"),
			Namespace: "ns2",
			Labels: map[string]string{
				labelManagedBy:       labelManagedByValue,
				labelInstrumentation: "my-inst",
				labelRule:            "rule-a",
			},
		},
	}
	// Unrelated ConfigMap — should not be deleted.
	unrelated := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated",
			Namespace: "ns1",
		},
	}

	r, cli := newReconciler(cm1, cm2, unrelated)

	err := r.deleteAllConfigMaps(context.Background(), "my-inst")
	require.NoError(t, err)

	// Managed ConfigMaps should be gone.
	var got corev1.ConfigMap
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(cm1), &got)
	assert.True(t, apierrors.IsNotFound(err), "cm1 should have been deleted")
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(cm2), &got)
	assert.True(t, apierrors.IsNotFound(err), "cm2 should have been deleted")

	// Unrelated ConfigMap should still exist.
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(unrelated), &got)
	require.NoError(t, err, "unrelated ConfigMap should still exist")
}

func TestReconcile_Deletion_CleansUpConfigMaps(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	now := metav1.Now()
	inst := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-inst",
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
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

	// ConfigMap that should be cleaned up during deletion.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName("my-inst", "rule-a"),
			Namespace: "default",
			Labels: map[string]string{
				labelManagedBy:       labelManagedByValue,
				labelInstrumentation: "my-inst",
				labelRule:            "rule-a",
			},
		},
		Data: map[string]string{configMapDataKey: "file_format: \"1.0\"\n"},
	}

	r, cli := newReconciler(ns, inst, cm)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(inst),
	})
	require.NoError(t, err)

	// ConfigMap should be deleted.
	var got corev1.ConfigMap
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(cm), &got)
	assert.True(t, apierrors.IsNotFound(err), "ConfigMap should have been deleted during CR deletion")

	// After finalizer removal, the fake client may garbage-collect the object
	// (DeletionTimestamp set + no finalizers = deleted). Either outcome is correct.
	var updated v2alpha1.Instrumentation
	err = cli.Get(context.Background(), client.ObjectKeyFromObject(inst), &updated)
	if err == nil {
		assert.NotContains(t, updated.Finalizers, finalizerName)
	} else {
		assert.True(t, apierrors.IsNotFound(err), "object should be gone after finalizer removal")
	}
}

func TestReconcile_DeletionWithoutFinalizer_Noop(t *testing.T) {
	now := metav1.Now()
	inst := &v2alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "my-inst",
			DeletionTimestamp: &now,
			Finalizers:        []string{"some-other-finalizer"}, // not ours
		},
	}

	r, _ := newReconciler(inst)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(inst),
	})
	require.NoError(t, err)
}

func TestReconcile_NotFound_Noop(t *testing.T) {
	r, _ := newReconciler()

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Name: "nonexistent"},
	})
	require.NoError(t, err)
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
