// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package injector

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	"github.com/open-telemetry/opentelemetry-operator/apis/v2alpha1"
)

const (
	finalizerName = "injector.opentelemetry.io/configmap-cleanup"

	labelManagedBy       = "app.kubernetes.io/managed-by"
	labelManagedByValue  = "opentelemetry-operator"
	labelInstrumentation = "opentelemetry.io/instrumentation"
	labelRule            = "opentelemetry.io/rule"

	configMapDataKey = "otel-config.yaml"
)

// InstrumentationReconciler reconciles v2alpha1 Instrumentation CRs,
// managing ConfigMaps for rules with declarativeConfig.
type InstrumentationReconciler struct {
	client.Client
	scheme *runtime.Scheme
	log    logr.Logger
}

// NewInstrumentationReconciler creates a new reconciler.
func NewInstrumentationReconciler(c client.Client, scheme *runtime.Scheme, log logr.Logger) *InstrumentationReconciler {
	return &InstrumentationReconciler{
		Client: c,
		scheme: scheme,
		log:    log,
	}
}

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=instrumentation.opentelemetry.io,resources=instrumentations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=instrumentation.opentelemetry.io,resources=instrumentations/status,verbs=get;update;patch

// Reconcile manages ConfigMaps for each rule with declarativeConfig.
func (r *InstrumentationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.log.WithValues("instrumentation", req.Name)

	var inst v2alpha1.Instrumentation
	if err := r.Get(ctx, req.NamespacedName, &inst); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion via finalizer.
	if inst.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, log, &inst)
	}

	// Ensure finalizer is present if any rule has declarativeConfig.
	if needsFinalizer(inst) {
		if controllerutil.AddFinalizer(&inst, finalizerName) {
			if err := r.Update(ctx, &inst); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Collect the set of desired ConfigMaps.
	desired, err := r.buildDesiredConfigMaps(ctx, &inst)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Upsert desired ConfigMaps.
	for key, cm := range desired {
		log.V(1).Info("upserting ConfigMap", "namespace", key.Namespace, "name", key.Name)
		if err := r.upsertConfigMap(ctx, cm); err != nil {
			upsertErr := fmt.Errorf("upserting ConfigMap %s/%s: %w", key.Namespace, key.Name, err)
			_ = r.setStatus(ctx, &inst, "ReconcileError", metav1.ConditionFalse, upsertErr.Error())
			return ctrl.Result{}, upsertErr
		}
	}

	// Prune stale ConfigMaps no longer desired.
	if err := r.pruneStaleConfigMaps(ctx, log, &inst, desired); err != nil {
		return ctrl.Result{}, r.setStatus(ctx, &inst, "ReconcileError", metav1.ConditionFalse, err.Error())
	}

	if err := r.setStatus(ctx, &inst, "Reconciled", metav1.ConditionTrue, fmt.Sprintf("%d rules, %d ConfigMaps", len(inst.Spec.Rules), len(desired))); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller.
func (r *InstrumentationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v2alpha1.Instrumentation{}).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueAllInstrumentations),
		).
		Complete(r)
}

// enqueueAllInstrumentations re-reconciles every Instrumentation CR when a namespace
// is created/updated, so catch-all rules (empty namespace selector) can propagate.
func (r *InstrumentationReconciler) enqueueAllInstrumentations(_ context.Context, _ client.Object) []reconcile.Request {
	ctx := context.Background()
	var list v2alpha1.InstrumentationList
	if err := r.List(ctx, &list); err != nil {
		r.log.Error(err, "failed to list Instrumentation CRs for namespace watch")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(list.Items))
	for _, inst := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&inst),
		})
	}
	return requests
}

// handleDeletion cleans up all ConfigMaps owned by this CR, then removes the finalizer.
func (r *InstrumentationReconciler) handleDeletion(ctx context.Context, log logr.Logger, inst *v2alpha1.Instrumentation) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(inst, finalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("cleaning up ConfigMaps for deleted Instrumentation")
	if err := r.deleteAllConfigMaps(ctx, inst.Name); err != nil {
		return ctrl.Result{}, err
	}

	// Re-fetch to avoid conflicts.
	latest := &v2alpha1.Instrumentation{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), latest); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(latest, finalizerName)
	if err := r.Update(ctx, latest); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// buildDesiredConfigMaps computes the full set of ConfigMaps that should exist.
func (r *InstrumentationReconciler) buildDesiredConfigMaps(ctx context.Context, inst *v2alpha1.Instrumentation) (map[client.ObjectKey]*corev1.ConfigMap, error) {
	desired := make(map[client.ObjectKey]*corev1.ConfigMap)

	for _, rule := range inst.Spec.Rules {
		if rule.Config.DeclarativeConfig == nil {
			continue
		}

		data, err := yaml.Marshal(rule.Config.DeclarativeConfig.Object)
		if err != nil {
			return nil, fmt.Errorf("marshaling declarativeConfig for rule %q: %w", rule.Name, err)
		}

		namespaces, err := r.resolveNamespaces(ctx, rule.Selector.Namespaces)
		if err != nil {
			return nil, err
		}

		cmName := ConfigMapName(inst.Name, rule.Name)
		for _, ns := range namespaces {
			key := client.ObjectKey{Namespace: ns, Name: cmName}
			desired[key] = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: ns,
					Labels: map[string]string{
						labelManagedBy:       labelManagedByValue,
						labelInstrumentation: inst.Name,
						labelRule:            rule.Name,
					},
				},
				Data: map[string]string{
					configMapDataKey: string(data),
				},
			}
		}
	}

	return desired, nil
}

// resolveNamespaces returns the list of target namespaces. If explicit is empty,
// returns all existing namespaces except Kubernetes system namespaces (catch-all).
func (r *InstrumentationReconciler) resolveNamespaces(ctx context.Context, explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		return explicit, nil
	}

	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList); err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}

	namespaces := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		if isSystemNamespace(ns.Name) {
			continue
		}
		namespaces = append(namespaces, ns.Name)
	}
	return namespaces, nil
}

// isSystemNamespace returns true for Kubernetes system namespaces that should
// not receive injected ConfigMaps from catch-all rules.
func isSystemNamespace(name string) bool {
	return strings.HasPrefix(name, "kube-")
}

// upsertConfigMap creates or updates a ConfigMap.
func (r *InstrumentationReconciler) upsertConfigMap(ctx context.Context, desired *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	existing.Labels = desired.Labels
	existing.Data = desired.Data
	return r.Update(ctx, existing)
}

// pruneStaleConfigMaps deletes ConfigMaps that are labeled for this CR but not in the desired set.
func (r *InstrumentationReconciler) pruneStaleConfigMaps(ctx context.Context, log logr.Logger, inst *v2alpha1.Instrumentation, desired map[client.ObjectKey]*corev1.ConfigMap) error {
	var cmList corev1.ConfigMapList
	if err := r.List(ctx, &cmList, client.MatchingLabels{
		labelManagedBy:       labelManagedByValue,
		labelInstrumentation: inst.Name,
	}); err != nil {
		return fmt.Errorf("listing managed ConfigMaps: %w", err)
	}

	for i := range cmList.Items {
		cm := &cmList.Items[i]
		key := client.ObjectKeyFromObject(cm)
		if _, ok := desired[key]; !ok {
			log.V(1).Info("deleting stale ConfigMap", "namespace", cm.Namespace, "name", cm.Name)
			if err := r.Delete(ctx, cm); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting stale ConfigMap %s/%s: %w", cm.Namespace, cm.Name, err)
			}
		}
	}

	return nil
}

// deleteAllConfigMaps removes all ConfigMaps labeled for the given Instrumentation CR name.
func (r *InstrumentationReconciler) deleteAllConfigMaps(ctx context.Context, instName string) error {
	return r.DeleteAllOf(ctx, &corev1.ConfigMap{}, client.MatchingLabels{
		labelManagedBy:       labelManagedByValue,
		labelInstrumentation: instName,
	})
}

// setStatus updates the Ready condition on the Instrumentation CR.
func (r *InstrumentationReconciler) setStatus(ctx context.Context, inst *v2alpha1.Instrumentation, reason string, status metav1.ConditionStatus, message string) error {
	meta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: inst.Generation,
		Reason:             reason,
		Message:            message,
	})
	return r.Status().Update(ctx, inst)
}

// needsFinalizer returns true if any rule has declarativeConfig.
func needsFinalizer(inst v2alpha1.Instrumentation) bool {
	for _, rule := range inst.Spec.Rules {
		if rule.Config.DeclarativeConfig != nil {
			return true
		}
	}
	return false
}

// ConfigMapName returns the deterministic ConfigMap name for a given CR + rule.
// Exported so the webhook can reference the same name.
func ConfigMapName(instName, ruleName string) string {
	name := fmt.Sprintf("otel-injector-%s-%s", instName, ruleName)
	if len(name) > 253 {
		name = truncateWithHash(name, 253)
	}
	return name
}
