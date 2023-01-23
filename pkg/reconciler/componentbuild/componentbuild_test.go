package componentbuild

import (
	"context"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/artifactbuild"
	"github.com/stuartwdouglas/apheleia/pkg/apis/apheleia/v1alpha1"
	aph "github.com/stuartwdouglas/apheleia/pkg/client/clientset/versioned/scheme"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"testing"

	quotav1 "github.com/openshift/api/quota/v1"
	fakequotaclientset "github.com/openshift/client-go/quota/clientset/versioned/fake"
	jbs "github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/clusterresourcequota"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"

	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	namespace = "default"
	name      = "test"
	artifact  = "com.test:test:1.0"
)

func setupClientAndReconciler(objs ...runtimeclient.Object) (runtimeclient.Client, *ReconcileArtifactBuild) {
	scheme := runtime.NewScheme()
	_ = jbs.AddToScheme(scheme)
	_ = v1beta1.AddToScheme(scheme)
	_ = aph.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)
	_ = quotav1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	clusterresourcequota.QuotaClient = fakequotaclientset.NewSimpleClientset()
	reconciler := &ReconcileArtifactBuild{client: client, scheme: scheme, eventRecorder: &record.FakeRecorder{}}
	return client, reconciler
}

func TestArtifactBuildCreation(t *testing.T) {
	g := NewGomegaWithT(t)
	client, reconciler := setupClientAndReconciler()
	cb := defaultComponentBuild()
	ctx := context.TODO()
	err := client.Create(ctx, &cb)
	g.Expect(err).NotTo(HaveOccurred())
	var result reconcile.Result
	result, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).NotTo(BeNil())

	ab := jbs.ArtifactBuild{}
	err = client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: artifactbuild.CreateABRName(artifact)}, &ab)
	g.Expect(err).NotTo(HaveOccurred())

	//now lets complete the artifact build

}

func defaultComponentBuild() v1alpha1.ComponentBuild {
	return v1alpha1.ComponentBuild{
		ObjectMeta: controllerruntime.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1alpha1.ComponentBuildSpec{
			SCMURL:    "https://test.com/test.git",
			Tag:       "1.0",
			Artifacts: []string{artifact},
		},
	}
}
