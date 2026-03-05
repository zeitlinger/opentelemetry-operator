// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package v2alpha1

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// +kubebuilder:webhook:verbs=create;update,path=/validate-opentelemetry-io-v2alpha1-instrumentation,mutating=false,failurePolicy=fail,groups=opentelemetry.io,resources=instrumentations,versions=v2alpha1,name=vinstrumentationv2createupdate.kb.io,sideEffects=none,admissionReviewVersions=v1
// +kubebuilder:webhook:verbs=delete,path=/validate-opentelemetry-io-v2alpha1-instrumentation,mutating=false,failurePolicy=ignore,groups=opentelemetry.io,resources=instrumentations,versions=v2alpha1,name=vinstrumentationv2delete.kb.io,sideEffects=none,admissionReviewVersions=v1
// +kubebuilder:object:generate=false

// InstrumentationWebhookV2 validates v2alpha1 Instrumentation CRs.
// imageVolumeBlockedReason is non-empty when the cluster does not meet the
// requirements for image volumes (Kubernetes 1.32+ and containerd 2.1+).
// An empty string means image volumes are supported and CRs may be created.
type InstrumentationWebhookV2 struct {
	log                      logr.Logger
	imageVolumeBlockedReason string
}

var _ admission.CustomValidator = &InstrumentationWebhookV2{}

// dnsLabelRegexp matches valid DNS label characters (lowercase alphanum + hyphens, no leading/trailing hyphen).
var dnsLabelRegexp = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// SetupInstrumentationWebhook registers the v2alpha1 Instrumentation validator.
// imageVolumeBlockedReason is forwarded from injector.CheckImageVolumeSupport:
// if non-empty, all create/update requests are rejected with that reason.
func SetupInstrumentationWebhook(mgr ctrl.Manager, imageVolumeBlockedReason string) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&Instrumentation{}).
		WithValidator(&InstrumentationWebhookV2{
			log:                      ctrl.Log.WithName("webhook").WithName("Instrumentation"),
			imageVolumeBlockedReason: imageVolumeBlockedReason,
		}).
		Complete()
}

func (w InstrumentationWebhookV2) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	inst, ok := obj.(*Instrumentation)
	if !ok {
		return nil, fmt.Errorf("expected an Instrumentation, received %T", obj)
	}
	if w.imageVolumeBlockedReason != "" {
		w.log.Info("rejected Instrumentation create: image volumes not supported",
			"name", inst.Name, "reason", w.imageVolumeBlockedReason)
		return nil, fmt.Errorf("cannot create Instrumentation: %s", w.imageVolumeBlockedReason)
	}
	return validate(inst)
}

func (w InstrumentationWebhookV2) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	inst, ok := newObj.(*Instrumentation)
	if !ok {
		return nil, fmt.Errorf("expected an Instrumentation, received %T", newObj)
	}
	if w.imageVolumeBlockedReason != "" {
		w.log.Info("rejected Instrumentation update: image volumes not supported",
			"name", inst.Name, "reason", w.imageVolumeBlockedReason)
		return nil, fmt.Errorf("cannot update Instrumentation: %s", w.imageVolumeBlockedReason)
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
	if inst.Spec.Injector == "" {
		errs = append(errs, "spec.injector must be non-empty")
	}

	// 2. Defaults mode validation.
	if inst.Spec.Defaults.Mode != nil {
		switch *inst.Spec.Defaults.Mode {
		case InstrumentationModeInstall, InstrumentationModeSkip, InstrumentationModeInstallUnlessConflict:
			// valid
		default:
			errs = append(errs, fmt.Sprintf("spec.defaults.mode: invalid mode %q (must be install, skip, or install_unless_conflict)", *inst.Spec.Defaults.Mode))
		}
	}

	// 3. Duplicate rule names.
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

		// 6. Mode validation.
		if rule.Config.Mode != nil {
			switch *rule.Config.Mode {
			case InstrumentationModeInstall, InstrumentationModeSkip, InstrumentationModeInstallUnlessConflict:
				// valid
			default:
				errs = append(errs, fmt.Sprintf("%s: invalid mode %q (must be install, skip, or install_unless_conflict)", prefix, *rule.Config.Mode))
			}
		}

		// 7. Skip + declarativeConfig conflict.
		if rule.Config.Mode != nil && *rule.Config.Mode == InstrumentationModeSkip && rule.Config.DeclarativeConfig != nil {
			errs = append(errs, fmt.Sprintf("%s: mode skip must not have declarativeConfig (config would be created but never mounted)", prefix))
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid Instrumentation: %s", strings.Join(errs, "; "))
	}
	return nil, nil
}
