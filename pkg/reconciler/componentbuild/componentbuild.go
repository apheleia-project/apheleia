package componentbuild

import (
	"context"
	"fmt"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stuartwdouglas/apheleia/pkg/apis/apheleia/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"knative.dev/pkg/apis"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"

	"github.com/go-logr/logr"
	jvmbs "github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/artifactbuild"
)

const (
	//TODO eventually we'll need to decide if we want to make this tuneable
	contextTimeout  = 300 * time.Second
	DeployTaskLabel = "apheleia.io/deploy-task"
)

type ReconcileArtifactBuild struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

func newReconciler(mgr ctrl.Manager) reconcile.Reconciler {
	return &ReconcileArtifactBuild{
		client:        mgr.GetClient(),
		scheme:        mgr.GetScheme(),
		eventRecorder: mgr.GetEventRecorderFor("ComponentBuild"),
	}
}

func (r *ReconcileArtifactBuild) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	// Set the ctx to be Background, as the top-level context for incoming requests.
	var cancel context.CancelFunc
	if request.ClusterName != "" {
		// use logicalcluster.ClusterFromContxt(ctx) to retrieve this value later on
		ctx = logicalcluster.WithCluster(ctx, logicalcluster.New(request.ClusterName))
	}
	ctx, cancel = context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	log := ctrl.Log.WithName("artifactbuild").WithValues("request", request.NamespacedName).WithValues("cluster", request.ClusterName)

	abr := jvmbs.ArtifactBuild{}
	abrerr := r.client.Get(ctx, request.NamespacedName, &abr)
	if abrerr != nil {
		if !errors.IsNotFound(abrerr) {
			log.Error(abrerr, "Reconcile key %s as artifactbuild unexpected error", request.NamespacedName.String())
			return ctrl.Result{}, abrerr
		}
	}

	cb := v1alpha1.ComponentBuild{}
	cberr := r.client.Get(ctx, request.NamespacedName, &cb)
	if cberr != nil {
		if !errors.IsNotFound(cberr) {
			log.Error(cberr, "Reconcile key %s as componentbuild unexpected error", request.NamespacedName.String())
			return ctrl.Result{}, cberr
		}
	}

	if cberr != nil && abrerr != nil {
		msg := "Reconcile key received not found errors for componentbuilds, artifactbuilds (probably deleted): " + request.NamespacedName.String()
		log.Info(msg)
		return ctrl.Result{}, nil
	}

	switch {
	case cberr == nil:
		return r.handleComponentBuildReceived(ctx, log, &cb)

	case abrerr == nil:
		return r.handleArtifactBuildReceived(ctx, log, &abr)
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileArtifactBuild) handleComponentBuildReceived(ctx context.Context, log logr.Logger, cb *v1alpha1.ComponentBuild) (reconcile.Result, error) {
	completed := cb.Status.State == v1alpha1.ComponentBuildStateComplete || cb.Status.State == v1alpha1.ComponentBuildStateFailed
	if completed {
		if !cb.Status.ArtifactsDeployed {
			err := r.deployArtifacts(ctx, log, cb)
			if err != nil {
				return reconcile.Result{}, err
			}
			cb.Status.ResultNotified = true
			return reconcile.Result{}, r.client.Status().Update(ctx, cb)
		} else if !cb.Status.ResultNotified {
			err := r.notifyResult(ctx, log, cb)
			if err != nil {
				return reconcile.Result{}, err
			}
			cb.Status.ResultNotified = true
			return reconcile.Result{}, r.client.Status().Update(ctx, cb)
		}
	}

	abrMap := map[string]*jvmbs.ArtifactBuild{}
	abrList := jvmbs.ArtifactBuildList{}
	err := r.client.List(ctx, &abrList, client.InNamespace(cb.Namespace))
	if err != nil {
		return reconcile.Result{}, err
	}
	for i := range abrList.Items {
		item := abrList.Items[i]
		abrMap[item.Spec.GAV] = &item
	}

	//iterate over the spec, and calculate the corresponding status
	cb.Status.Outstanding = 0
	oldState := cb.Status.ArtifactState
	cb.Status.ArtifactState = map[string]v1alpha1.ArtifactState{}
	//TODO: Handle contaminates
	for _, i := range cb.Spec.Artifacts {
		existing := abrMap[i]
		if existing != nil {
			cb.Status.ArtifactState[i] = artifactState(existing)
			_, existingRef := oldState[i]
			if !existingRef {
				//add an owner ref if not already present
				err := controllerutil.SetOwnerReference(cb, existing, r.scheme)
				if err != nil {
					return reconcile.Result{}, err
				}
				err = r.client.Update(ctx, existing)
				if err != nil {
					return reconcile.Result{}, err
				}
			}
			if !cb.Status.ArtifactState[i].Done {
				cb.Status.Outstanding++
			}
		} else {
			abr := jvmbs.ArtifactBuild{}
			abr.Spec = jvmbs.ArtifactBuildSpec{GAV: i}
			abr.Name = artifactbuild.CreateABRName(i)
			abr.Namespace = cb.Namespace
			err := controllerutil.SetOwnerReference(cb, &abr, r.scheme)
			if err != nil {
				return reconcile.Result{}, err
			}
			err = r.client.Create(ctx, &abr)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
	}
	if cb.Status.Outstanding > 0 {
		//if there are still some outstanding we reset the notification state
		cb.Status.ResultNotified = false
	}
	return reconcile.Result{}, r.client.Status().Update(ctx, cb)
}

func (r *ReconcileArtifactBuild) notifyResult(ctx context.Context, log logr.Logger, cb *v1alpha1.ComponentBuild) error {
	//TODO: KICK OFF JENKINS RUN HERE
	return nil
}

// Attempts to deploy all the artifacts from the namespace
// Note that this is a generic 'deploy all' task that it is running
// so other artifacts might be deployed as well
func (r *ReconcileArtifactBuild) deployArtifacts(ctx context.Context, log logr.Logger, cb *v1alpha1.ComponentBuild) error {
	//first look for an existing TaskRun
	existing := v1beta1.TaskRunList{}
	listOpts := &client.ListOptions{
		Namespace:     cb.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{DeployTaskLabel: cb.Name}),
	}
	err := r.client.List(ctx, &existing, listOpts)
	if err != nil {
		return err
	}
	success := false
	for _, t := range existing.Items {
		if t.Status.CompletionTime != nil {
			success = t.Status.GetCondition(apis.ConditionSucceeded).IsTrue()
			if !success {
				cb.Status.Message = fmt.Sprintf("Deploy failed, check TaskRun %s. To retry delete the TaskRun", t.Name)
				return r.client.Status().Update(ctx, cb)
			}
		}
	}
	if success {
		cb.Status.ArtifactsDeployed = true
		cb.Status.Message = ""
		return r.client.Status().Update(ctx, cb)
	}
	//now we need a TaskRun

	tr := v1beta1.TaskRun{}
	tr.GenerateName = cb.Name
	tr.Namespace = cb.Namespace
	tr.Labels = map[string]string{DeployTaskLabel: cb.Name}
	tr.Spec.TaskRef = &v1beta1.TaskRef{Name: "apheleia-deploy", Kind: v1beta1.ClusterTaskKind}
	tr.Spec.Params = []v1beta1.Param{
		{Name: "DOMAIN", Value: v1beta1.ArrayOrString{StringVal: "rhosak", Type: v1beta1.ParamTypeString}},
		{Name: "OWNER", Value: v1beta1.ArrayOrString{StringVal: "237843776254", Type: v1beta1.ParamTypeString}},
		{Name: "REPO", Value: v1beta1.ArrayOrString{StringVal: "https://rhosak-237843776254.d.codeartifact.us-east-2.amazonaws.com/maven/sdouglas-scratch/", Type: v1beta1.ParamTypeString}},
		{Name: "FORCE", Value: v1beta1.ArrayOrString{StringVal: "false", Type: v1beta1.ParamTypeString}},
	}
	return r.client.Create(ctx, &tr)
}
func (r *ReconcileArtifactBuild) handleArtifactBuildReceived(ctx context.Context, log logr.Logger, abr *jvmbs.ArtifactBuild) (reconcile.Result, error) {
	cbList := v1alpha1.ComponentBuildList{}
	err := r.client.List(ctx, &cbList, client.InNamespace(abr.Namespace))
	if err != nil {
		return reconcile.Result{}, err
	}
	artifactState := artifactState(abr)
	for _, i := range cbList.Items {
		v, exists := i.Status.ArtifactState[abr.Spec.GAV]
		if exists {
			done := v.Done
			i.Status.ArtifactState[abr.Spec.GAV] = artifactState
			if !done && artifactState.Done {
				i.Status.Outstanding--
			} else if done && !artifactState.Done {
				i.Status.Outstanding++
			}
			if i.Status.Outstanding == 0 {
				failed := false
				for _, v := range i.Status.ArtifactState {
					if v.Failed {
						failed = true
						break
					}
				}
				if failed {
					i.Status.State = v1alpha1.ComponentBuildStateFailed
				} else {
					i.Status.State = v1alpha1.ComponentBuildStateComplete
				}
			} else {
				i.Status.State = v1alpha1.ComponentBuildStateInProgress
			}
			err := r.client.Status().Update(ctx, &i)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
	}
	return reconcile.Result{}, nil
}

func artifactState(abr *jvmbs.ArtifactBuild) v1alpha1.ArtifactState {
	failed := abr.Status.State == jvmbs.ArtifactBuildStateFailed || abr.Status.State == jvmbs.ArtifactBuildStateMissing
	done := failed || abr.Status.State == jvmbs.ArtifactBuildStateComplete
	return v1alpha1.ArtifactState{ArtifactBuild: abr.Name, Failed: failed, Done: done}
}
