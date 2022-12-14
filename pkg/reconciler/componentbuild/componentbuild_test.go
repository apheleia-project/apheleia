package componentbuild

import (
	"context"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/systemconfig"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/pendingpipelinerun"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/util"
	pipelinev1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"knative.dev/pkg/apis/duck/v1beta1"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const gav = "com.acme:foo:1.0"
const repo = "https://github.com/foo.git"
const version = "1.0"
const otherName = "other-artifact"
const name = "test"

func setupClientAndReconciler(objs ...runtimeclient.Object) (runtimeclient.Client, *ReconcileArtifactBuild) {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = pipelinev1beta1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	sysConfig := &v1alpha1.JBSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.JBSConfigName, Namespace: metav1.NamespaceDefault},
		Spec: v1alpha1.JBSConfigSpec{
			EnableRebuilds:       true,
			VerifyBuiltArtifacts: true,
		},
	}
	systemConfig := &v1alpha1.SystemConfig{
		ObjectMeta: metav1.ObjectMeta{Name: systemconfig.SystemConfigKey},
		Spec:       v1alpha1.SystemConfigSpec{},
	}
	objs = append(objs, sysConfig, systemConfig)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	reconciler := &ReconcileArtifactBuild{client: client, scheme: scheme, eventRecorder: &record.FakeRecorder{}, prCreator: &pendingpipelinerun.ImmediateCreate{}}
	util.ImageTag = "foo"
	return client, reconciler
}

func TestArtifactBuildStateNew(t *testing.T) {
	g := NewGomegaWithT(t)
	abr := v1alpha1.ArtifactBuild{}
	abr.Namespace = metav1.NamespaceDefault
	abr.Name = "test"
	abr.Status.State = v1alpha1.ArtifactBuildStateNew

	ctx := context.TODO()
	client, reconciler := setupClientAndReconciler(&abr)
	const customRepo = "https://myrepo.com/repo.git"
	sysConfig := v1alpha1.JBSConfig{}
	g.Expect(client.Get(ctx, types.NamespacedName{Name: v1alpha1.JBSConfigName, Namespace: metav1.NamespaceDefault}, &sysConfig)).Should(Succeed())
	sysConfig.Spec.AdditionalRecipes = []string{customRepo}

	g.Expect(client.Update(context.TODO(), &sysConfig)).Should(Succeed())

	g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: abr.Namespace, Name: abr.Name}}))

	g.Expect(client.Get(ctx, types.NamespacedName{
		Namespace: metav1.NamespaceDefault,
		Name:      "test",
	}, &abr))
	g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateDiscovering))

	trList := &pipelinev1beta1.PipelineRunList{}
	g.Expect(client.List(ctx, trList))
	g.Expect(len(trList.Items)).Should(Equal(1))
	for _, tr := range trList.Items {
		for _, or := range tr.OwnerReferences {
			g.Expect(or.Kind).Should(Equal(abr.Kind))
			g.Expect(or.Name).Should(Equal(abr.Name))
		}
		g.Expect(tr.Spec.PipelineSpec.Tasks[0].TaskSpec.Steps[0].Args[2]).Should(ContainSubstring(customRepo))
	}
}

func getABR(client runtimeclient.Client, g *WithT) *v1alpha1.ArtifactBuild {
	return getNamedABR(client, g, "test")
}

func getNamedABR(client runtimeclient.Client, g *WithT, name string) *v1alpha1.ArtifactBuild {
	ctx := context.TODO()
	abr := v1alpha1.ArtifactBuild{}
	g.Expect(client.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: name}, &abr)).To(BeNil())
	return &abr
}

func TestStateDiscovering(t *testing.T) {
	ctx := context.TODO()

	fullValidation := func(client runtimeclient.Client, g *WithT) {
		abr := getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateBuilding))

		dbList := v1alpha1.DependencyBuildList{}
		g.Expect(client.List(context.TODO(), &dbList))
		g.Expect(dbList.Items).Should(Not(BeEmpty()))
		for _, db := range dbList.Items {
			for _, or := range db.OwnerReferences {
				g.Expect(or.Kind).Should(Equal(abr.Kind))
				g.Expect(or.Name).Should(Equal(abr.Name))
			}
			g.Expect(db.Spec.ScmInfo.Tag).Should(Equal("foo"))
			g.Expect(db.Spec.ScmInfo.SCMURL).Should(Equal("goo"))
			g.Expect(db.Spec.ScmInfo.SCMType).Should(Equal("hoo"))
			g.Expect(db.Spec.ScmInfo.Path).Should(Equal("ioo"))
			g.Expect(db.Spec.Version).Should(Equal("1.0"))

			g.Expect(abr.Status.SCMInfo.Tag).Should(Equal("foo"))
			g.Expect(abr.Status.SCMInfo.SCMURL).Should(Equal("goo"))
			g.Expect(abr.Status.SCMInfo.SCMType).Should(Equal("hoo"))
			g.Expect(abr.Status.SCMInfo.Path).Should(Equal("ioo"))
		}
	}

	var client runtimeclient.Client
	var reconciler *ReconcileArtifactBuild
	now := metav1.Now()
	setup := func() {
		abr := &v1alpha1.ArtifactBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: metav1.NamespaceDefault,
			},
			Spec: v1alpha1.ArtifactBuildSpec{GAV: gav},
			Status: v1alpha1.ArtifactBuildStatus{
				State: v1alpha1.ArtifactBuildStateDiscovering,
			},
		}
		client, reconciler = setupClientAndReconciler(abr)
	}
	t.Run("SCM tag cannot be determined", func(t *testing.T) {
		g := NewGomegaWithT(t)
		setup()
		tr := &pipelinev1beta1.PipelineRun{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-tr",
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{ArtifactBuildIdLabel: ABRLabelForGAV(gav)},
			},
			Spec: pipelinev1beta1.PipelineRunSpec{},
			Status: pipelinev1beta1.PipelineRunStatus{
				Status:                  v1beta1.Status{},
				PipelineRunStatusFields: pipelinev1beta1.PipelineRunStatusFields{CompletionTime: &now},
			},
		}
		g.Expect(client.Create(ctx, tr))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr := getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateDiscovering))
		g.Expect(controllerutil.SetOwnerReference(abr, tr, reconciler.scheme))
		g.Expect(client.Update(ctx, tr))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test-tr"}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateMissing))
	})
	t.Run("First ABR creates DependencyBuild", func(t *testing.T) {
		g := NewGomegaWithT(t)
		setup()
		tr := &pipelinev1beta1.PipelineRun{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-tr",
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{ArtifactBuildIdLabel: ABRLabelForGAV(gav)},
			},
			Spec: pipelinev1beta1.PipelineRunSpec{},
			Status: pipelinev1beta1.PipelineRunStatus{
				Status: v1beta1.Status{},
				PipelineRunStatusFields: pipelinev1beta1.PipelineRunStatusFields{CompletionTime: &now, PipelineResults: []pipelinev1beta1.PipelineRunResult{
					{Name: PipelineResultScmTag, Value: "foo"},
					{Name: PipelineResultScmUrl, Value: "goo"},
					{Name: PipelineResultScmType, Value: "hoo"},
					{Name: PipelineResultContextPath, Value: "ioo"}}},
			},
		}
		abr := getABR(client, g)
		g.Expect(controllerutil.SetOwnerReference(abr, tr, reconciler.scheme))
		g.Expect(client.Create(ctx, tr))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test-tr"}}))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr = getABR(client, g)
		depId := hashString(abr.Status.SCMInfo.SCMURL + abr.Status.SCMInfo.Tag + abr.Status.SCMInfo.Path)
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: depId}}))
		fullValidation(client, g)
	})
	t.Run("DependencyBuild already exists for ABR", func(t *testing.T) {
		g := NewGomegaWithT(t)
		g.Expect(client.Create(ctx, &pipelinev1beta1.PipelineRun{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{ArtifactBuildIdLabel: ABRLabelForGAV(gav)},
			},
			Spec: pipelinev1beta1.PipelineRunSpec{},
			Status: pipelinev1beta1.PipelineRunStatus{
				Status: v1beta1.Status{},
				PipelineRunStatusFields: pipelinev1beta1.PipelineRunStatusFields{CompletionTime: &now, PipelineResults: []pipelinev1beta1.PipelineRunResult{
					{Name: PipelineResultScmTag, Value: "foo"},
					{Name: PipelineResultScmUrl, Value: "goo"},
					{Name: PipelineResultScmType, Value: "hoo"},
					{Name: PipelineResultContextPath, Value: "ioo"}}},
			},
		}))
		g.Expect(client.Create(ctx, &v1alpha1.DependencyBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-generated",
				Namespace: metav1.NamespaceDefault,
			},
			Spec: v1alpha1.DependencyBuildSpec{ScmInfo: v1alpha1.SCMInfo{
				Tag:     "foo",
				SCMURL:  "goo",
				SCMType: "hoo",
				Path:    "ioo",
			},
				Version: "1.0"},
		}))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		fullValidation(client, g)
	})
}

func TestStateBuilding(t *testing.T) {
	ctx := context.TODO()
	var client runtimeclient.Client
	var reconciler *ReconcileArtifactBuild
	setup := func() {
		abr := &v1alpha1.ArtifactBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{DependencyBuildIdLabel: "test"},
			},
			Spec: v1alpha1.ArtifactBuildSpec{},
			Status: v1alpha1.ArtifactBuildStatus{
				State: v1alpha1.ArtifactBuildStateBuilding,
			},
		}
		other := &v1alpha1.ArtifactBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      otherName,
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{DependencyBuildIdLabel: "test"},
			},
			Spec: v1alpha1.ArtifactBuildSpec{},
			Status: v1alpha1.ArtifactBuildStatus{
				State: v1alpha1.ArtifactBuildStateBuilding,
			},
		}
		client, reconciler = setupClientAndReconciler(abr, other)
	}
	t.Run("Failed build", func(t *testing.T) {
		g := NewGomegaWithT(t)
		setup()
		abr := getABR(client, g)
		depId := hashString(abr.Status.SCMInfo.SCMURL + abr.Status.SCMInfo.Tag + abr.Status.SCMInfo.Path)
		db := &v1alpha1.DependencyBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      depId,
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{DependencyBuildIdLabel: hashString("")},
			},
			Spec:   v1alpha1.DependencyBuildSpec{},
			Status: v1alpha1.DependencyBuildStatus{State: v1alpha1.DependencyBuildStateFailed},
		}
		g.Expect(controllerutil.SetOwnerReference(abr, db, reconciler.scheme))
		g.Expect(client.Create(ctx, db))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateFailed))
	})
	t.Run("Completed build", func(t *testing.T) {
		g := NewGomegaWithT(t)
		setup()
		abr := getABR(client, g)
		depId := hashString(abr.Status.SCMInfo.SCMURL + abr.Status.SCMInfo.Tag + abr.Status.SCMInfo.Path)
		db := &v1alpha1.DependencyBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      depId,
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{DependencyBuildIdLabel: hashString("")},
			},
			Spec:   v1alpha1.DependencyBuildSpec{},
			Status: v1alpha1.DependencyBuildStatus{State: v1alpha1.DependencyBuildStateComplete, DeployedArtifacts: []string{abr.Spec.GAV}},
		}
		g.Expect(controllerutil.SetOwnerReference(abr, db, reconciler.scheme))
		g.Expect(client.Create(ctx, db))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateComplete))
	})
	t.Run("Failed build that is reset", func(t *testing.T) {
		g := NewGomegaWithT(t)
		setup()
		abr := getABR(client, g)
		abr.Status.SCMInfo.SCMURL = repo
		abr.Status.SCMInfo.Tag = version
		g.Expect(client.Update(ctx, abr))

		other := getNamedABR(client, g, otherName)
		other.Status.SCMInfo.SCMURL = repo
		other.Status.SCMInfo.Tag = version
		g.Expect(client.Update(ctx, other))
		depId := hashString(abr.Status.SCMInfo.SCMURL + abr.Status.SCMInfo.Tag + abr.Status.SCMInfo.Path)
		db := v1alpha1.DependencyBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      depId,
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{DependencyBuildIdLabel: hashString("")},
			},
			Spec:   v1alpha1.DependencyBuildSpec{},
			Status: v1alpha1.DependencyBuildStatus{State: v1alpha1.DependencyBuildStateFailed},
		}
		g.Expect(controllerutil.SetOwnerReference(abr, &db, reconciler.scheme))
		g.Expect(controllerutil.SetOwnerReference(other, &db, reconciler.scheme))
		g.Expect(client.Create(ctx, &db))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateFailed))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: otherName}}))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: otherName}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateFailed))
		abr.Annotations = map[string]string{Rebuild: "true"}
		g.Expect(client.Update(ctx, abr))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: otherName}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateNew))
		g.Expect(abr.Annotations[Rebuild]).Should(Equal("true")) //first reconcile does not remove the annotation
		err := client.Get(ctx, types.NamespacedName{Name: db.Name, Namespace: db.Namespace}, &db)
		g.Expect(errors.IsNotFound(err)).Should(BeTrue())
		other = getNamedABR(client, g, otherName)
		g.Expect(other.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateNew))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateNew))
		g.Expect(abr.Annotations[Rebuild]).Should(Equal("")) //second reconcile removes the annotation
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateDiscovering)) //3rd reconcile kicks off discovery

	})
	t.Run("Contaminated build", func(t *testing.T) {
		g := NewGomegaWithT(t)
		setup()
		abr := getABR(client, g)
		depId := hashString(abr.Status.SCMInfo.SCMURL + abr.Status.SCMInfo.Tag + abr.Status.SCMInfo.Path)
		db := &v1alpha1.DependencyBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      depId,
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{DependencyBuildIdLabel: hashString("")},
			},
			Spec:   v1alpha1.DependencyBuildSpec{},
			Status: v1alpha1.DependencyBuildStatus{State: v1alpha1.DependencyBuildStateContaminated, Contaminants: []v1alpha1.Contaminant{{GAV: "com.test:test:1.0", ContaminatedArtifacts: []string{"a:b:1"}}}},
		}
		g.Expect(controllerutil.SetOwnerReference(abr, db, reconciler.scheme))
		g.Expect(client.Create(ctx, db))
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr = getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateFailed))
	})
	t.Run("Missing (deleted) build", func(t *testing.T) {
		g := NewGomegaWithT(t)
		setup()
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		abr := getABR(client, g)
		g.Expect(abr.Status.State).Should(Equal(v1alpha1.ArtifactBuildStateNew))
	})
}

func TestStateCompleteFixingContamination(t *testing.T) {
	ctx := context.TODO()
	var client runtimeclient.Client
	var reconciler *ReconcileArtifactBuild
	contaminatedName := "contaminated-build"
	setup := func() {
		abr := &v1alpha1.ArtifactBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{DependencyBuildIdLabel: "test"},
				Annotations: map[string]string{
					DependencyBuildContaminatedBy + "suffix": contaminatedName,
				},
			},
			Spec: v1alpha1.ArtifactBuildSpec{GAV: "com.test:test:1.0"},
			Status: v1alpha1.ArtifactBuildStatus{
				State: v1alpha1.ArtifactBuildStateComplete,
			},
		}
		contaiminated := &v1alpha1.DependencyBuild{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:      contaminatedName,
				Namespace: metav1.NamespaceDefault,
				Labels:    map[string]string{DependencyBuildIdLabel: hashString("")},
			},
			Spec:   v1alpha1.DependencyBuildSpec{},
			Status: v1alpha1.DependencyBuildStatus{State: v1alpha1.DependencyBuildStateContaminated, Contaminants: []v1alpha1.Contaminant{{GAV: "com.test:test:1.0", ContaminatedArtifacts: []string{"a:b:1"}}}},
		}
		client, reconciler = setupClientAndReconciler(abr, contaiminated)
	}
	t.Run("Test contamination cleared", func(t *testing.T) {
		g := NewGomegaWithT(t)
		setup()
		g.Expect(reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "test"}}))
		db := v1alpha1.DependencyBuild{}
		g.Expect(client.Get(ctx, types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: contaminatedName}, &db))
		g.Expect(db.Status.Contaminants).Should(BeEmpty())
	})
}
