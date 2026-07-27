// Package v1alpha1 holds k8s-dencer's API types.
//
// Deliberately outside internal/ so other tools can import them.
//
// +kubebuilder:object:generate=true
// +groupName=dencer.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is the API group and version for k8s-dencer's types.
var GroupVersion = schema.GroupVersion{Group: "dencer.io", Version: "v1alpha1"}

// SchemeBuilder registers these types with a runtime scheme.
var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

// AddToScheme adds these types to a scheme.
var AddToScheme = SchemeBuilder.AddToScheme
