// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package v2alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstrumentationSpec defines the desired state of Instrumentation.
type InstrumentationSpec struct {
	// Injector configures the composite SDK image and injection behavior.
	// +optional
	Injector InjectorSpec `json:"injector,omitempty"`
}

// InjectorSpec defines the injector configuration.
type InjectorSpec struct {
	// Image is the composite SDK image containing all language agents and the injector.
	// +optional
	Image string `json:"image,omitempty"`
}

// Instrumentation is the Schema for the instrumentations API.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=instr2
type Instrumentation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec InstrumentationSpec `json:"spec,omitempty"`
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
