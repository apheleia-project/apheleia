package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ComponentBuildStateFailed     = "ComponentBuildFailed"
	ComponentBuildStateComplete   = "ComponentBuildComplete"
	ComponentBuildStateInProgress = "ComponentBuildBuildInProgress"
)

type ComponentBuildSpec struct {
	SCMURL    string   `json:"scmURL,omitempty"`
	PRURL     string   `json:"prURL,omitempty"`
	Tag       string   `json:"tag,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

type ComponentBuildStatus struct {
	State          string                   `json:"state,omitempty"`
	Outstanding    int                      `json:"outstanding,omitempty"`
	ArtifactState  map[string]ArtifactState `json:"artifactState,omitempty"`
	Message        string                   `json:"message,omitempty"`
	ResultNotified bool                     `json:"resultNotified,omitempty"`
}

//type ArtifactBuildState string

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=componentbuilds,scope=Namespaced
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.scmURL`
// +kubebuilder:printcolumn:name="Tag",type=string,JSONPath=`.spec.tag`
// +kubebuilder:printcolumn:name="Outstanding",type=integer,JSONPath=`.status.outstanding`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
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
	ArtifactBuild string `json:"artifactBuild,omitempty"`
	Built         bool   `json:"built,omitempty"`
	Deployed      bool   `json:"deployed,omitempty"`
	Failed        bool   `json:"failed,omitempty"`
}

func (as *ArtifactState) Done() bool {
	if as.Built && as.Deployed {
		return true
	}
	return false
}
