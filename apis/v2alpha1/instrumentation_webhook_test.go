// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package v2alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidate_ValidCR(t *testing.T) {
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			Injector: "injector:latest",
			Rules: []Rule{
				{
					Name: "catch-all",
					Config: RuleConfig{
						Env: []corev1.EnvVar{
							{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://collector:4318"},
						},
					},
				},
			},
		},
	}

	w := InstrumentationWebhookV2{}
	_, err := w.ValidateCreate(context.Background(), inst)
	assert.NoError(t, err)
}

func TestValidate_EmptyInjectorImage(t *testing.T) {
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			Rules: []Rule{{Name: "catch-all"}},
		},
	}

	_, err := validate(inst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.injector must be non-empty")
}

func TestValidate_DuplicateRuleNames(t *testing.T) {
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			Injector: "injector:latest",
			Rules: []Rule{
				{Name: "my-rule"},
				{Name: "my-rule"},
			},
		},
	}

	_, err := validate(inst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate rule name \"my-rule\"")
}

func TestValidate_DuplicateEmptyNamesAllowed(t *testing.T) {
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			Injector: "injector:latest",
			Rules: []Rule{
				{Selector: RuleSelector{Namespaces: []string{"a"}}},
				{Selector: RuleSelector{Namespaces: []string{"b"}}},
			},
		},
	}

	_, err := validate(inst)
	assert.NoError(t, err)
}

func TestValidate_ReservedEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		envName string
		errMsg  string
	}{
		{"OTEL_INJECTOR_ prefix", "OTEL_INJECTOR_SERVICE_NAME", "OTEL_INJECTOR_"},
		{"OTEL_CONFIG_FILE", "OTEL_CONFIG_FILE", "OTEL_CONFIG_FILE"},
		{"OTEL_EXPERIMENTAL_CONFIG_FILE", "OTEL_EXPERIMENTAL_CONFIG_FILE", "OTEL_EXPERIMENTAL_CONFIG_FILE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instrumentation{
				Spec: InstrumentationSpec{
					Injector: "injector:latest",
					Rules: []Rule{
						{
							Name: "bad",
							Config: RuleConfig{
								Env: []corev1.EnvVar{{Name: tt.envName, Value: "x"}},
							},
						},
					},
				},
			}

			_, err := validate(inst)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestValidate_DeclarativeConfigRequiresName(t *testing.T) {
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			Injector: "injector:latest",
			Rules: []Rule{
				{
					Config: RuleConfig{
						DeclarativeConfig: &DeclarativeConfig{
							Object: map[string]any{"file_format": "1.0"},
						},
					},
				},
			},
		},
	}

	_, err := validate(inst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declarativeConfig requires a rule name")
}

func TestValidate_RuleNameDNSCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		rule    string
		wantErr bool
	}{
		{"valid", "my-config", false},
		{"uppercase", "MyConfig", true},
		{"underscore", "my_config", true},
		{"leading-hyphen", "-config", true},
		{"trailing-hyphen", "config-", true},
		{"single-char", "a", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instrumentation{
				Spec: InstrumentationSpec{
					Injector: "injector:latest",
					Rules: []Rule{
						{
							Name: tt.rule,
							Config: RuleConfig{
								DeclarativeConfig: &DeclarativeConfig{
									Object: map[string]any{"file_format": "1.0"},
								},
							},
						},
					},
				},
			}

			_, err := validate(inst)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "DNS label")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidate_DisabledWithDeclarativeConfig(t *testing.T) {
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			Injector: "injector:latest",
			Rules: []Rule{
				{
					Name: "conflicting",
					Config: RuleConfig{
						Disabled: true,
						DeclarativeConfig: &DeclarativeConfig{
							Object: map[string]any{"file_format": "1.0"},
						},
					},
				},
			},
		},
	}

	_, err := validate(inst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled rule must not have declarativeConfig")
}

func TestValidate_MultipleErrors(t *testing.T) {
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			// Missing image + other errors
			Rules: []Rule{
				{
					Name: "bad",
					Config: RuleConfig{
						Disabled: true,
						DeclarativeConfig: &DeclarativeConfig{
							Object: map[string]any{"file_format": "1.0"},
						},
						Env: []corev1.EnvVar{{Name: "OTEL_INJECTOR_FOO", Value: "x"}},
					},
				},
			},
		},
	}

	_, err := validate(inst)
	require.Error(t, err)
	// Should report all errors, not just the first.
	assert.Contains(t, err.Error(), "spec.injector")
	assert.Contains(t, err.Error(), "OTEL_INJECTOR_")
	assert.Contains(t, err.Error(), "disabled rule must not have declarativeConfig")
}

func TestValidate_UpdateSameAsCreate(t *testing.T) {
	inst := &Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: InstrumentationSpec{
			Injector: "injector:latest",
			Rules:    []Rule{{Name: "ok"}},
		},
	}

	w := InstrumentationWebhookV2{}
	_, err := w.ValidateUpdate(context.Background(), inst, inst)
	assert.NoError(t, err)
}

func TestValidate_DeleteAlwaysAllowed(t *testing.T) {
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			// Invalid — but delete should still succeed.
		},
	}

	w := InstrumentationWebhookV2{}
	_, err := w.ValidateDelete(context.Background(), inst)
	assert.NoError(t, err)
}

func TestValidate_RuleNameWithoutDeclarativeConfig_SkipsDNSCheck(t *testing.T) {
	// Rule names without declarativeConfig don't need DNS compatibility
	// since they're not used for ConfigMap naming.
	inst := &Instrumentation{
		Spec: InstrumentationSpec{
			Injector: "injector:latest",
			Rules: []Rule{
				{Name: "My_Fancy_Rule"},
			},
		},
	}

	_, err := validate(inst)
	assert.NoError(t, err)
}
