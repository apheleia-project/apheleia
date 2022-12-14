package v1alpha1

import (
	"github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ComponentBuildFailed     = "ComponentBuildFailed"
	ComponentBuildComplete   = "ComponentBuildComplete"
	ComponentBuildInProgress = "ComponentBuildBuildInProgress"
)

type ComponentBuildSpec struct {
	v1alpha1.SCMInfo `json:",inline"`
	Artifacts        []string `json:"artifacts,omitempty"`
}

type ComponentBuildStatus struct {
	State         string                   `json:"state,omitempty"`
	Outstanding   int                      `json:"outstanding,omitempty"`
	ArtifactState map[string]ArtifactState `json:"artifactState,omitempty"`
	Message       string                   `json:"message,omitempty"`
}

//type ArtifactBuildState string

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=componentbuilds,scope=Namespaced
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.scmURL`
// +kubebuilder:printcolumn:name="Tag",type=string,JSONPath=`.spec.tag`
// +kubebuilder:printcolumn:name="Outstanding",type=int,JSONPath=`.status.outstanding`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// ComponentBuild A build of an upstream component
type ComponentBuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ComponentBuildSpec   `json:"spec"`
	Status ComponentBuildStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ComponentBuildList contains a list of ComponentBuild
type ComponentBuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ComponentBuild `json:"items"`
}

type ArtifactState struct {
	SCMURL  string `json:"scmURL,omitempty"`
	SCMType string `json:"scmType,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Path    string `json:"path,omitempty"`
	Private bool   `json:"private,omitempty"`
}
