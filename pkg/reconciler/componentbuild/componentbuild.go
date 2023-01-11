package componentbuild

import (
	"context"
	"fmt"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stuartwdouglas/apheleia/pkg/apis/apheleia/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"knative.dev/pkg/apis"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"strings"
	"time"

	"github.com/go-logr/logr"
	jvmbs "github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
	"github.com/redhat-appstudio/jvm-build-service/pkg/reconciler/artifactbuild"
)

const (
	//TODO eventually we'll need to decide if we want to make this tuneable
	contextTimeout      = 300 * time.Second
	DeployTaskLabel     = "apheleia.io/deploy-task"
	NotifyPipelineLabel = "apheleia.io/notify-pipeline"
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

	tr := v1beta1.TaskRun{}
	trerr := r.client.Get(ctx, request.NamespacedName, &cb)
	if trerr != nil {
		if !errors.IsNotFound(trerr) {
			log.Error(trerr, "Reconcile key %s as componentbuild unexpected error", request.NamespacedName.String())
			return ctrl.Result{}, trerr
		}
	}
	if cberr != nil && abrerr != nil && trerr != nil {
		msg := "Reconcile key received not found errors for componentbuilds, artifactbuilds (probably deleted): " + request.NamespacedName.String()
		log.Info(msg)
		return ctrl.Result{}, nil
	}

	switch {
	case cberr == nil:
		return r.handleComponentBuildReceived(ctx, log, &cb)

	case abrerr == nil:
		return r.handleArtifactBuildReceived(ctx, log, &abr)
	case trerr == nil:
		return r.handleTaskRunReceived(ctx, log, &tr)
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileArtifactBuild) handleComponentBuildReceived(ctx context.Context, log logr.Logger, cb *v1alpha1.ComponentBuild) (reconcile.Result, error) {
	log.Info("Handling ComponentBuild", "name", cb.Name, "outstanding", cb.Status.Outstanding, "state", cb.Status.State)

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
			cb.Status.ArtifactState[i] = artifactState(&abr)
			cb.Status.Outstanding++
		}
	}
	if cb.Status.Outstanding == 0 {
		//completed, change the state
		failed := false
		for _, v := range cb.Status.ArtifactState {
			if v.Failed {
				failed = true
				break
			}
		}
		if failed {
			cb.Status.State = v1alpha1.ComponentBuildStateFailed
		} else {
			cb.Status.State = v1alpha1.ComponentBuildStateComplete
		}
		//this had previously had all its parts built, nothing has changed
		//so we check if we need to run the deploy tasks
		if !cb.Status.ArtifactsDeployed {
			log.Info("Deploying ComponentBuild Artifacts", "name", cb.Name, "outstanding", cb.Status.Outstanding, "state", cb.Status.State)
			err := r.deployArtifacts(ctx, log, cb)
			if err != nil {
				return reconcile.Result{}, err
			}
			cb.Status.ArtifactsDeployed = true
			return reconcile.Result{}, r.client.Status().Update(ctx, cb)
		} else if !cb.Status.ResultNotified {
			err := r.notifyResult(ctx, log, cb)
			if err != nil {
				return reconcile.Result{}, err
			}
			cb.Status.ResultNotified = true
			return reconcile.Result{}, r.client.Status().Update(ctx, cb)
		}
	} else {
		//if there are still some outstanding we reset the notification state
		cb.Status.ResultNotified = false
		cb.Status.State = v1alpha1.ComponentBuildStateInProgress
		cb.Status.ArtifactsDeployed = false
		cb.Status.ResultNotified = false
	}
	err = r.client.Status().Update(ctx, cb)
	return reconcile.Result{}, err
}

func (r *ReconcileArtifactBuild) notifyResult(ctx context.Context, log logr.Logger, cb *v1alpha1.ComponentBuild) error {
	tr := v1beta1.PipelineRun{}
	tr.GenerateName = cb.Name + "-notify-pipeline"
	tr.Namespace = cb.Namespace
	tr.Labels = map[string]string{NotifyPipelineLabel: cb.Name}
	var notifierMessage string
	if cb.Status.State == v1alpha1.ComponentBuildStateFailed {
		var failedGavs []string
		for gav, v := range cb.Status.ArtifactState {
			if v.Failed {
				failedGavs = append(failedGavs, gav)
			}
		}
		notifierMessage = fmt.Sprintf("The following dependency builds have failed: %s.", strings.Join(failedGavs[:], ","))
	} else if cb.Status.State == v1alpha1.ComponentBuildStateComplete {
		notifierMessage = "/retest Success all dependency builds have completed."
	}
	tr.Spec.PipelineRef = &v1beta1.PipelineRef{Name: "component-build-notifier"}
	tr.Spec.Params = []v1beta1.Param{
		{Name: "url", Value: v1beta1.ArrayOrString{StringVal: cb.Spec.SCMURL, Type: v1beta1.ParamTypeString}},
		{Name: "secret-key-ref", Value: v1beta1.ArrayOrString{StringVal: "jvm-build-git-secrets", Type: v1beta1.ParamTypeString}},
		{Name: "message", Value: v1beta1.ArrayOrString{StringVal: notifierMessage, Type: v1beta1.ParamTypeString}},
	}
	qty, err := resource.ParseQuantity("1Gi")
	if err != nil {
		return err
	}
	tr.Spec.Workspaces = []v1beta1.WorkspaceBinding{
		{Name: "pr", VolumeClaimTemplate: &v1.PersistentVolumeClaim{
			Spec: v1.PersistentVolumeClaimSpec{
				AccessModes: []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
				Resources: v1.ResourceRequirements{
					Requests: map[v1.ResourceName]resource.Quantity{"storage": qty},
				},
			},
		}},
	}
	log.Info("Notifying ComponentBuild Status Update via PR Comment", "name", cb.Name, "scmUrl", cb.Spec.SCMURL, "state", cb.Status.State)
	return r.client.Create(ctx, &tr)
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
	tr.GenerateName = cb.Name + "-deploy-task"
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
		_, exists := i.Status.ArtifactState[abr.Spec.GAV]
		if exists {
			i.Status.ArtifactState[abr.Spec.GAV] = artifactState
			err := r.client.Status().Update(ctx, &i)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
	}
	return reconcile.Result{}, nil
}

func (r *ReconcileArtifactBuild) handleTaskRunReceived(ctx context.Context, log logr.Logger, tr *v1beta1.TaskRun) (reconcile.Result, error) {

	if tr.Status.CompletionTime == nil {
		return reconcile.Result{}, nil
	}
	ownerRefs := tr.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		msg := "taskrun missing onwerrefs %s:%s"
		r.eventRecorder.Eventf(tr, v1.EventTypeWarning, msg, tr.Namespace, tr.Name)
		log.Info(msg, tr.Namespace, tr.Name)
		return reconcile.Result{}, nil
	}
	ownerName := ""
	for _, ownerRef := range ownerRefs {
		if strings.EqualFold(ownerRef.Kind, "componentbuild") || strings.EqualFold(ownerRef.Kind, "componentbuilds") {
			ownerName = ownerRef.Name
			break
		}
	}
	if len(ownerName) == 0 {
		msg := "taskrun missing componentbuild ownerrefs %s:%s"
		r.eventRecorder.Eventf(tr, v1.EventTypeWarning, "MissingOwner", msg, tr.Namespace, tr.Name)
		log.Info(msg, tr.Namespace, tr.Name)
		return reconcile.Result{}, nil
	}

	key := types.NamespacedName{Namespace: tr.Namespace, Name: ownerName}
	cb := v1alpha1.ComponentBuild{}
	err := r.client.Get(ctx, key, &cb)
	if err != nil {
		msg := "get for taskrun %s:%s owning component build %s:%s yielded error %s"
		r.eventRecorder.Eventf(tr, v1.EventTypeWarning, msg, tr.Namespace, tr.Name, tr.Namespace, ownerName, err.Error())
		log.Error(err, fmt.Sprintf(msg, tr.Namespace, tr.Name, tr.Namespace, ownerName, err.Error()))
		return reconcile.Result{}, err
	}
	return r.handleComponentBuildReceived(ctx, log, &cb)
}

func artifactState(abr *jvmbs.ArtifactBuild) v1alpha1.ArtifactState {
	failed := abr.Status.State == jvmbs.ArtifactBuildStateFailed || abr.Status.State == jvmbs.ArtifactBuildStateMissing
	done := failed || abr.Status.State == jvmbs.ArtifactBuildStateComplete
	return v1alpha1.ArtifactState{ArtifactBuild: abr.Name, Failed: failed, Done: done}
}
