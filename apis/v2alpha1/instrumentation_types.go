// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package v2alpha1

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstrumentationSpec defines the desired state of Instrumentation.
//
// Image configuration lives at the spec level (not on individual rules) because
// agent versions are a platform-level concern: all rules in a CR share the same
// agent versions. To use different agent versions for different environments,
// create separate Instrumentation CRs with different priority values.
type InstrumentationSpec struct {
	// Priority determines which Instrumentation CR wins when multiple CRs match a pod.
	// Higher values take precedence. Creation timestamp is used as a tiebreaker.
	// +optional
	Priority int `json:"priority,omitempty"`

	// Injector is the injector binary image (libotelinject.so + otelinject.conf).
	// This image is always required; without it no instrumentation occurs.
	// +optional
	Injector string `json:"injector,omitempty"`

	// Java is the Java agent image. When set, the operator runs a dedicated init
	// container for Java instrumentation.
	// +optional
	Java string `json:"java,omitempty"`

	// NodeJS is the Node.js agent image. When set, the operator runs a dedicated
	// init container for Node.js instrumentation.
	// +optional
	NodeJS string `json:"nodejs,omitempty"`

	// Python is the Python agent image. When set, the operator runs a dedicated
	// init container for Python instrumentation.
	// +optional
	Python string `json:"python,omitempty"`

	// DotNet is the .NET agent image. When set, the operator runs a dedicated
	// init container for .NET instrumentation.
	// +optional
	DotNet string `json:"dotnet,omitempty"`

	// Rules is an ordered list of instrumentation rules. Rules are evaluated
	// sequentially per container; the first matching rule is applied. If no rule
	// matches, no instrumentation is applied.
	// +optional
	Rules []Rule `json:"rules,omitempty"`
}

// Rule defines a single instrumentation rule consisting of a selector and the
// configuration to apply when the selector matches.
type Rule struct {
	// Name is an optional human-readable label for this rule, used for debugging
	// and status reporting.
	// +optional
	Name string `json:"name,omitempty"`

	// Selector determines which (pod, container) pairs this rule applies to.
	// An empty selector matches all containers in all pods in all namespaces.
	// +optional
	Selector RuleSelector `json:"selector,omitempty"`

	// Config defines what to do when this rule matches.
	// +optional
	Config RuleConfig `json:"config,omitempty"`
}

// RuleSelector defines the dimensions used to match containers for a rule.
// All specified dimensions must match (AND semantics). Matching is evaluated
// per container: namespace and pod labels are checked at the pod level, then
// container name is checked per container.
type RuleSelector struct {
	// Namespaces is a list of namespaces in which this rule applies.
	// An empty list matches all namespaces.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// PodLabels is a map of label key/value pairs. A pod must have all specified
	// labels to match (AND semantics). An empty map matches all pods.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// ContainerNames is a list of container names within matching pods to instrument.
	// An empty list matches all containers. Use this to apply different instrumentation
	// config to different containers in a multi-language pod.
	// +optional
	ContainerNames []string `json:"containerNames,omitempty"`
}

// RuleConfig defines what to do when a rule matches.
type RuleConfig struct {
	// Disabled, when true, suppresses instrumentation for matching containers.
	// Use this to explicitly opt out specific containers from broader catch-all rules
	// by placing a more specific disabled rule earlier in the list.
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// Env is a list of environment variables to inject into instrumented containers.
	// Supports all Kubernetes EnvVar sources including valueFrom.secretKeyRef.
	// Ignored when Disabled is true.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// DeclarativeConfig is an inline OpenTelemetry declarative configuration document
	// (file_format: "1.0"). The operator mounts it as a ConfigMap volume and sets
	// OTEL_CONFIG_FILE on each instrumented container. Use ${ENV_VAR} substitution
	// syntax within the document to reference secrets injected via Env.
	//
	// Note: SDKs ignore OTEL_* environment variables when a config file is present;
	// all SDK configuration must be expressed within this document. Use ${ENV_VAR}
	// substitution to bridge secrets from Env into the config file.
	//
	// Ignored when Disabled is true.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	DeclarativeConfig *DeclarativeConfig `json:"declarativeConfig,omitempty"`
}

// DeclarativeConfig holds an OpenTelemetry declarative configuration document as
// structured YAML/JSON. Users write native YAML nested under this field; the operator
// mounts the result as a file without interpreting the contents.
type DeclarativeConfig struct {
	Object map[string]any `json:"-" yaml:",inline"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *DeclarativeConfig) UnmarshalJSON(b []byte) error {
	vals := map[string]any{}
	if err := json.Unmarshal(b, &vals); err != nil {
		return err
	}
	d.Object = vals
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d *DeclarativeConfig) MarshalJSON() ([]byte, error) {
	if d == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(d.Object)
}

// DeepCopyInto copies the receiver into out. Manual implementation required
// because controller-gen cannot handle map[string]any.
func (d *DeclarativeConfig) DeepCopyInto(out *DeclarativeConfig) {
	*out = *d
	if d.Object != nil {
		out.Object = deepCopyMap(d.Object)
	}
}

// deepCopyMap recursively deep-copies a map[string]any, handling nested maps
// and slices that are common in OTel declarative config documents.
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return deepCopyMap(val)
	case []any:
		cp := make([]any, len(val))
		for i, item := range val {
			cp[i] = deepCopyValue(item)
		}
		return cp
	default:
		// Primitive types (string, float64, bool, nil) are safe to copy by value.
		return val
	}
}

// DeepCopy returns a deep copy of the receiver.
func (d *DeclarativeConfig) DeepCopy() *DeclarativeConfig {
	if d == nil {
		return nil
	}
	out := new(DeclarativeConfig)
	d.DeepCopyInto(out)
	return out
}

// InstrumentationStatus defines the observed state of an Instrumentation CR.
type InstrumentationStatus struct {
	// Conditions represent the latest available observations of the CR's state.
	// Known condition types: "Ready".
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Instrumentation is the Schema for the instrumentations API.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=instr2
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
type Instrumentation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstrumentationSpec   `json:"spec,omitempty"`
	Status InstrumentationStatus `json:"status,omitempty"`
}

// InstrumentationList contains a list of Instrumentation.
// +kubebuilder:object:root=true
type InstrumentationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instrumentation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instrumentation{}, &InstrumentationList{})
}
