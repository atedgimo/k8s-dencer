package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// MaintenanceWindowSpec declares when disruptive consolidation is permitted.
//
// This is the object that finally makes Red steps executable. Until it exists
// in a cluster, a Red step is refused outright — architecture doc §9 confines
// them to "an approved maintenance window", and the safe reading of that with
// no window defined is "never".
//
// Everything here is designed so that a mistake fails closed. An unparseable
// schedule, a missing timezone, an expired window: each one means "not open",
// never "open".
type MaintenanceWindowSpec struct {
	// Schedule is a standard five-field cron expression giving the moments the
	// window opens. Combined with Duration it defines the open intervals.
	//
	// Cron rather than a start time so a window can recur — "Sundays at 02:00"
	// is what operators actually have.
	//
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// Duration is how long the window stays open once it opens, e.g. "4h".
	//
	// +kubebuilder:validation:Pattern=`^([0-9]+h)?([0-9]+m)?$`
	// +kubebuilder:default="1h"
	Duration string `json:"duration,omitempty"`

	// TimeZone is an IANA name such as "Europe/London".
	//
	// Required, with no default. A window is a promise about wall-clock time in
	// somebody's working day, and silently interpreting "Sunday 02:00" as UTC
	// would open it at the wrong hour for most of the world — including, twice
	// a year, for the operator who wrote it.
	//
	// +kubebuilder:validation:MinLength=1
	TimeZone string `json:"timeZone"`

	// Suspend stops the window opening without deleting it. The obvious
	// control to reach for during an incident.
	//
	// +kubebuilder:default=false
	Suspend bool `json:"suspend,omitempty"`

	// AllowRed permits Red-rated steps while this window is open.
	//
	// Off by default, so creating a window does not by itself unlock the most
	// dangerous class of step. An operator who wants that must say so.
	//
	// +kubebuilder:default=false
	AllowRed bool `json:"allowRed,omitempty"`

	// NodeSelector limits the window to nodes carrying these labels. Empty
	// means every node.
	//
	// Lets a cluster have a permissive window for a batch pool and a strict one
	// everywhere else, which is how real maintenance policies are shaped.
	//
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// MaxNodes caps how many nodes may be drained across this window,
	// independent of the per-run cap. Zero means the run cap alone applies.
	//
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxNodes int32 `json:"maxNodes,omitempty"`
}

// MaintenanceWindowStatus reports whether the window is open.
//
// Eventually consistent, and NOT the authorisation. It is refreshed on a sweep
// (30s by default), so a window suspended a moment ago can still read Active
// here. The Safety Guard never consults it — it re-evaluates the spec against
// the clock on every check, because an authorisation read from a cache is not
// an authorisation. Status exists so `kubectl get mw` can answer "is this open"
// without anyone doing cron arithmetic in their head.
type MaintenanceWindowStatus struct {
	// Active is true while the window is currently open.
	Active bool `json:"active"`

	// NextOpen is when the window opens next, if it is currently closed.
	// +optional
	NextOpen *metav1.Time `json:"nextOpen,omitempty"`

	// ClosesAt is when the current opening ends, if it is open.
	// +optional
	ClosesAt *metav1.Time `json:"closesAt,omitempty"`

	// NodesDrained counts nodes drained during the current opening, for
	// MaxNodes.
	// +optional
	NodesDrained int32 `json:"nodesDrained,omitempty"`

	// Message explains the current state in words, including why a malformed
	// window is closed.
	// +optional
	Message string `json:"message,omitempty"`

	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MaintenanceWindow declares when disruptive consolidation may run.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=mw
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Duration",type=string,JSONPath=`.spec.duration`
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.timeZone`
// +kubebuilder:printcolumn:name="Red",type=boolean,JSONPath=`.spec.allowRed`
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=`.status.active`
// +kubebuilder:printcolumn:name="Closes",type=date,JSONPath=`.status.closesAt`
type MaintenanceWindow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MaintenanceWindowSpec   `json:"spec,omitempty"`
	Status MaintenanceWindowStatus `json:"status,omitempty"`
}

// MaintenanceWindowList is a list of windows.
//
// +kubebuilder:object:root=true
type MaintenanceWindowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MaintenanceWindow `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MaintenanceWindow{}, &MaintenanceWindowList{})
}

// Ensure the generated deepcopy satisfies runtime.Object.
var (
	_ runtime.Object = &MaintenanceWindow{}
	_ runtime.Object = &MaintenanceWindowList{}
)
