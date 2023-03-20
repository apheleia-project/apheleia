package componentbuild

import (
	"context"
	"github.com/apheleia-project/apheleia/pkg/apis/apheleia/v1alpha1"
	aph "github.com/apheleia-project/apheleia/pkg/client/clientset/versioned/scheme"
	. "github.com/onsi/gomega"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/artifactbuild"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/apis"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"testing"
	"time"

	quotav1 "github.com/openshift/api/quota/v1"
	jbs "github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	namespace   = "default"
	name        = "test"
	artifact    = "com.test:test:1.0"
	TestImage   = "test-image"
	DummyRepo   = "dummy-repo"
	DummyDomain = "dummy-domain"
	DummyOwner  = "dummy-owner"
)

func setupClientAndReconciler(objs ...runtimeclient.Object) (runtimeclient.Client, *ReconcileArtifactBuild) {
	scheme := runtime.NewScheme()
	_ = jbs.AddToScheme(scheme)
	_ = v1beta1.AddToScheme(scheme)
	_ = aph.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)
	_ = quotav1.AddToScheme(scheme)
	cm := v1.ConfigMap{}
	cm.Namespace = namespace
	cm.Name = ApheleiaConfig
	cm.Data = map[string]string{MavenRepo: DummyRepo, AWSDomain: DummyDomain, AWSOwner: DummyOwner}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithObjects(&cm).Build()
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
	abrName := types.NamespacedName{Namespace: namespace, Name: artifactbuild.CreateABRName(artifact)}
	err = client.Get(ctx, abrName, &ab)
	g.Expect(err).NotTo(HaveOccurred())

	//now let's complete the artifact build
	ab.Status.State = jbs.ArtifactBuildStateComplete
	g.Expect(client.Status().Update(ctx, &ab)).NotTo(HaveOccurred())

	db := jbs.DependencyBuild{}
	db.Namespace = abrName.Namespace
	db.Name = "test-db"
	g.Expect(controllerutil.SetOwnerReference(&ab, &db, client.Scheme())).NotTo(HaveOccurred())
	g.Expect(client.Create(ctx, &db)).NotTo(HaveOccurred())

	ra := jbs.RebuiltArtifact{}
	ra.Name = abrName.Name
	ra.Namespace = abrName.Namespace
	ra.Spec.Image = TestImage
	ra.Spec.GAV = ab.Spec.GAV
	g.Expect(controllerutil.SetOwnerReference(&db, &ra, client.Scheme())).NotTo(HaveOccurred())
	g.Expect(client.Create(ctx, &ra)).NotTo(HaveOccurred())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: abrName})
	g.Expect(err).NotTo(HaveOccurred())

	//now lets look for a taskrun
	trl := v1beta1.TaskRunList{}
	g.Expect(client.List(ctx, &trl)).NotTo(HaveOccurred())
	g.Expect(len(trl.Items)).To(Equal(1))
	tr := trl.Items[0]
	paramMap := map[string]string{}
	for _, p := range tr.Spec.Params {
		paramMap[p.Name] = p.Value.StringVal
	}
	g.Expect(paramMap["REPO"]).To(Equal(DummyRepo))
	g.Expect(paramMap["DOMAIN"]).To(Equal(DummyDomain))
	g.Expect(paramMap["OWNER"]).To(Equal(DummyOwner))

	tr.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	tr.Status.SetCondition(&apis.Condition{
		Type:               apis.ConditionSucceeded,
		Status:             "True",
		LastTransitionTime: apis.VolatileTime{Inner: metav1.Time{Time: time.Now()}},
	})
	g.Expect(client.Status().Update(ctx, &tr)).NotTo(HaveOccurred())

	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: tr.Namespace, Name: tr.Name}})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(client.Get(ctx, types.NamespacedName{Namespace: cb.Namespace, Name: cb.Name}, &cb)).NotTo(HaveOccurred())
	g.Expect(cb.Status.State).To(Equal("ComponentBuildComplete"))

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
