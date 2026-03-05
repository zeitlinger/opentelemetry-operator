// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package v2alpha1

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstrumentationMode controls whether a rule installs instrumentation.
// +kubebuilder:validation:Enum=install;skip;install_unless_conflict
type InstrumentationMode string

const (
	// InstrumentationModeInstall forces instrumentation even if existing manual
	// instrumentation is detected (e.g. Python sitecustomize, Node.js SDK imports).
	InstrumentationModeInstall InstrumentationMode = "install"

	// InstrumentationModeSkip suppresses instrumentation for matching containers.
	// Use this to explicitly opt out specific workloads from broader catch-all rules.
	InstrumentationModeSkip InstrumentationMode = "skip"

	// InstrumentationModeInstallUnlessConflict installs instrumentation unless the
	// injector detects existing manual instrumentation at runtime and backs off.
	// This is the default when no mode is specified.
	InstrumentationModeInstallUnlessConflict InstrumentationMode = "install_unless_conflict"
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

	// Defaults defines CR-wide default values that can be overridden per rule.
	// +optional
	Defaults InstrumentationDefaults `json:"defaults,omitempty"`

	// Rules is an ordered list of instrumentation rules. Rules are evaluated
	// sequentially per container; the first matching rule is applied. If no rule
	// matches, no instrumentation is applied.
	// +optional
	Rules []Rule `json:"rules,omitempty"`
}

// InstrumentationDefaults defines CR-wide defaults that individual rules can override.
type InstrumentationDefaults struct {
	// Mode is the default instrumentation mode for all rules in this CR.
	// Individual rules can override this via config.mode.
	// If unset, defaults to InstallUnlessConflict.
	// +optional
	Mode *InstrumentationMode `json:"mode,omitempty"`

	// Rollback configures automatic crash-loop recovery. When enabled, the operator
	// detects workloads that enter CrashLoopBackOff after instrumentation and automatically
	// backs off by skipping injection and restarting the workload.
	// +optional
	Rollback *RollbackConfig `json:"rollback,omitempty"`
}

// RollbackConfig controls automatic crash-loop recovery behavior.
type RollbackConfig struct {
	// Enabled controls whether automatic rollback is active. Defaults to true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// GraceTime is how long a pod must be in CrashLoopBackOff before the operator
	// triggers a rollback. This avoids reacting to transient startup issues.
	// Defaults to 5m.
	// +optional
	GraceTime *metav1.Duration `json:"graceTime,omitempty"`

	// StabilityWindow is how long after injection the operator attributes crashes to
	// instrumentation. Crashes after this window are assumed unrelated.
	// Defaults to 1h.
	// +optional
	StabilityWindow *metav1.Duration `json:"stabilityWindow,omitempty"`
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
	// Mode overrides spec.defaults.mode for this rule.
	// Install forces instrumentation, Skip suppresses it, InstallUnlessConflict
	// (the default) lets the injector detect and avoid existing instrumentation.
	// +optional
	Mode *InstrumentationMode `json:"mode,omitempty"`

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

	// InstrumentedWorkloads tracks workloads that have been instrumented by this CR.
	// Maintained by the rollback controller; used for crash-loop recovery, pod bouncing
	// on CR updates, and operator internal telemetry.
	// +optional
	InstrumentedWorkloads []InstrumentedWorkload `json:"instrumentedWorkloads,omitempty"`
}

// InstrumentedWorkload records that a workload has been instrumented by this CR.
type InstrumentedWorkload struct {
	// WorkloadRef identifies the instrumented workload.
	WorkloadRef WorkloadReference `json:"workloadRef"`

	// RuleName is the name of the rule that matched this workload.
	// +optional
	RuleName string `json:"ruleName,omitempty"`

	// InstrumentedAt is when injection was first applied to this workload.
	InstrumentedAt metav1.Time `json:"instrumentedAt"`

	// CRGeneration is the CR's metadata.generation at the time of injection.
	// Used to detect CR spec changes for automatic recovery after rollback.
	CRGeneration int64 `json:"crGeneration"`

	// Rollback is set when the operator has rolled back instrumentation for this
	// workload due to crash-loop detection. Nil means the workload is healthy.
	// +optional
	Rollback *RollbackInfo `json:"rollback,omitempty"`
}

// WorkloadReference identifies a Kubernetes workload.
type WorkloadReference struct {
	// Kind is the workload kind (e.g. Deployment, StatefulSet, DaemonSet).
	Kind string `json:"kind"`

	// Namespace is the workload's namespace.
	Namespace string `json:"namespace"`

	// Name is the workload's name.
	Name string `json:"name"`
}

// RollbackInfo records details about a crash-loop rollback.
type RollbackInfo struct {
	// Reason is the Kubernetes container state reason that triggered the rollback
	// (e.g. CrashLoopBackOff, ImagePullBackOff).
	Reason string `json:"reason"`

	// RolledBackAt is when the rollback was triggered.
	RolledBackAt metav1.Time `json:"rolledBackAt"`
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
