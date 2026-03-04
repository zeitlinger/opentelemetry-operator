// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package v2alpha1

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:verbs=create;update,path=/validate-opentelemetry-io-v2alpha1-instrumentation,mutating=false,failurePolicy=fail,groups=opentelemetry.io,resources=instrumentations,versions=v2alpha1,name=vinstrumentationv2createupdate.kb.io,sideEffects=none,admissionReviewVersions=v1
// +kubebuilder:webhook:verbs=delete,path=/validate-opentelemetry-io-v2alpha1-instrumentation,mutating=false,failurePolicy=ignore,groups=opentelemetry.io,resources=instrumentations,versions=v2alpha1,name=vinstrumentationv2delete.kb.io,sideEffects=none,admissionReviewVersions=v1
// +kubebuilder:object:generate=false

type InstrumentationWebhookV2 struct{}

var _ admission.CustomValidator = &InstrumentationWebhookV2{}

// dnsLabelRegexp matches valid DNS label characters (lowercase alphanum + hyphens, no leading/trailing hyphen).
var dnsLabelRegexp = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func SetupInstrumentationWebhook(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&Instrumentation{}).
		WithValidator(&InstrumentationWebhookV2{}).
		Complete()
}

func (w InstrumentationWebhookV2) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	inst, ok := obj.(*Instrumentation)
	if !ok {
		return nil, fmt.Errorf("expected an Instrumentation, received %T", obj)
	}
	return validate(inst)
}

func (w InstrumentationWebhookV2) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	inst, ok := newObj.(*Instrumentation)
	if !ok {
		return nil, fmt.Errorf("expected an Instrumentation, received %T", newObj)
	}
	return validate(inst)
}

func (w InstrumentationWebhookV2) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// Always allow deletion — never block cleanup.
	return nil, nil
}

func validate(inst *Instrumentation) (admission.Warnings, error) {
	var errs []string

	// 1. Injector image is required.
	if inst.Spec.Injector.Image == "" {
		errs = append(errs, "spec.injector.image must be non-empty")
	}

	// 2. Duplicate rule names.
	seen := map[string]bool{}
	for i, rule := range inst.Spec.Rules {
		if rule.Name == "" {
			continue
		}
		if seen[rule.Name] {
			errs = append(errs, fmt.Sprintf("rules[%d]: duplicate rule name %q", i, rule.Name))
		}
		seen[rule.Name] = true
	}

	for i, rule := range inst.Spec.Rules {
		prefix := fmt.Sprintf("rules[%d]", i)
		if rule.Name != "" {
			prefix = fmt.Sprintf("rules[%d] (%s)", i, rule.Name)
		}

		// 3. Reserved env vars.
		for _, e := range rule.Config.Env {
			if strings.HasPrefix(e.Name, "OTEL_INJECTOR_") {
				errs = append(errs, fmt.Sprintf("%s: env var %q uses reserved OTEL_INJECTOR_ prefix", prefix, e.Name))
			}
			if e.Name == "OTEL_CONFIG_FILE" || e.Name == "OTEL_EXPERIMENTAL_CONFIG_FILE" {
				errs = append(errs, fmt.Sprintf("%s: env var %q is reserved (set automatically when declarativeConfig is present)", prefix, e.Name))
			}
		}

		// 4. DeclarativeConfig requires a rule name.
		if rule.Config.DeclarativeConfig != nil && rule.Name == "" {
			errs = append(errs, fmt.Sprintf("%s: declarativeConfig requires a rule name (used for ConfigMap naming)", prefix))
		}

		// 5. Rule name DNS compatibility when declarativeConfig is set.
		if rule.Config.DeclarativeConfig != nil && rule.Name != "" {
			if !dnsLabelRegexp.MatchString(rule.Name) {
				errs = append(errs, fmt.Sprintf("%s: rule name %q must be lowercase alphanumeric with hyphens (DNS label) when declarativeConfig is set", prefix, rule.Name))
			}
			if len(rule.Name) > 200 {
				errs = append(errs, fmt.Sprintf("%s: rule name %q exceeds 200 characters", prefix, rule.Name))
			}
		}

		// 6. Disabled + declarativeConfig conflict.
		if rule.Config.Disabled && rule.Config.DeclarativeConfig != nil {
			errs = append(errs, fmt.Sprintf("%s: disabled rule must not have declarativeConfig (config would be created but never mounted)", prefix))
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid Instrumentation: %s", strings.Join(errs, "; "))
	}
	return nil, nil
}
