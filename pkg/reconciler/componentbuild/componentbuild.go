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
	trerr := r.client.Get(ctx, request.NamespacedName, &tr)
	if trerr != nil {
		if !errors.IsNotFound(trerr) {
			log.Error(trerr, "Reconcile key %s as taskrun unexpected error", request.NamespacedName.String())
			return ctrl.Result{}, trerr
		}
	}
	pr := v1beta1.PipelineRun{}
	prerr := r.client.Get(ctx, request.NamespacedName, &pr)
	if prerr != nil {
		if !errors.IsNotFound(prerr) {
			log.Error(prerr, "Reconcile key %s as pipelinerun unexpected error", request.NamespacedName.String())
			return ctrl.Result{}, prerr
		}
	}
	if cberr != nil && abrerr != nil && trerr != nil && prerr != nil {
		msg := "Reconcile key received not found errors for componentbuilds, artifactbuilds, pipelineruns, taskruns (probably deleted): " + request.NamespacedName.String()
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
	case prerr == nil:
		return r.handlePipelineRunReceived(ctx, log, &pr)
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileArtifactBuild) handleComponentBuildReceived(ctx context.Context, log logr.Logger, cb *v1alpha1.ComponentBuild) (reconcile.Result, error) {
	log.Info("Handling ComponentBuild", "name", cb.Name, "outstanding", cb.Status.Outstanding, "state", cb.Status.State)

	//iterate over the spec, and calculate the corresponding status
	cb.Status.Outstanding = 0
	cb.Status.ArtifactState = map[string]v1alpha1.ArtifactState{}
	//TODO: Handle contaminates
	for _, i := range cb.Spec.Artifacts {
		existing := jvmbs.ArtifactBuild{}
		key := types.NamespacedName{Namespace: cb.Namespace, Name: artifactbuild.CreateABRName(i)}
		aberr := r.client.Get(ctx, key, &existing)
		if aberr == nil || !errors.IsNotFound(aberr) {
			cb.Status.ArtifactState[i] = r.artifactState(ctx, log, &existing)
			state := cb.Status.ArtifactState[i]
			if !state.Done() && !state.Failed {
				cb.Status.Outstanding++
			}
			if state.Built && !state.Deployed {
				derr := r.deployArtifact(ctx, log, &existing)
				if derr != nil {
					log.Error(derr, "Error deploying artifact", "name", existing.Name)
				}
			}
		} else {
			abr := jvmbs.ArtifactBuild{}
			abr.Spec = jvmbs.ArtifactBuildSpec{GAV: i}
			abr.Name = artifactbuild.CreateABRName(i)
			abr.Namespace = cb.Namespace
			err := r.client.Create(ctx, &abr)
			if err != nil {
				return reconcile.Result{}, err
			}
			cb.Status.ArtifactState[i] = r.artifactState(ctx, log, &abr)
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

		if (cb.Status.State == v1alpha1.ComponentBuildStateComplete || cb.Status.State == v1alpha1.ComponentBuildStateFailed) && !cb.Status.ResultNotified {
			err := r.notifyResult(ctx, log, cb)
			if err != nil {
				return reconcile.Result{}, err
			}
		}
	} else {
		//if there are still some outstanding we reset the notification state
		cb.Status.State = v1alpha1.ComponentBuildStateInProgress
		cb.Status.ResultNotified = false
	}
	err := r.client.Status().Update(ctx, cb)
	return reconcile.Result{}, err
}

func (r *ReconcileArtifactBuild) notifyResult(ctx context.Context, log logr.Logger, cb *v1alpha1.ComponentBuild) error {
	if cb.Spec.PRURL == "" {
		log.Info("Notifying ComponentBuild Status Skipped as PRURL is not set", "name", cb.Name, "scmUrl", cb.Spec.SCMURL, "state", cb.Status.State)
		return nil
	}
	//first look for an existing TaskRun - If none are found create a new one
	existing := v1beta1.PipelineRunList{}
	listOpts := &client.ListOptions{
		Namespace:     cb.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{NotifyPipelineLabel: cb.Name}),
	}
	err := r.client.List(ctx, &existing, listOpts)
	if err != nil {
		return err
	}
	for _, i := range existing.Items {
		if i.Status.GetCondition(apis.ConditionSucceeded).IsUnknown() {
			return nil
		}
	}
	tr := &v1beta1.PipelineRun{}
	tr.GenerateName = cb.Name + "-notify-pipeline"
	tr.Namespace = cb.Namespace
	cerr := controllerutil.SetControllerReference(cb, tr, r.scheme)
	if cerr != nil {
		log.Error(cerr, fmt.Sprintf("Error setting controller reference for pipelinerun %s", tr.Name))
	}
	tr.Labels = map[string]string{NotifyPipelineLabel: cb.Name}
	var notifierMessage string
	if cb.Status.State == v1alpha1.ComponentBuildStateFailed {
		var failedGavs []string
		for gav, v := range cb.Status.ArtifactState {
			if v.Failed {
				failedGavs = append(failedGavs, gav)
			}
		}
		notifierMessage = fmt.Sprintf("The following dependency builds have failed: %s.", strings.Join(failedGavs[:], ", "))
	} else if cb.Status.State == v1alpha1.ComponentBuildStateComplete {
		notifierMessage = "/retest Success all dependency builds have completed."
	}
	tr.Spec.PipelineRef = &v1beta1.PipelineRef{Name: "component-build-notifier"}
	tr.Spec.Params = []v1beta1.Param{
		{Name: "url", Value: v1beta1.ArrayOrString{StringVal: cb.Spec.PRURL, Type: v1beta1.ParamTypeString}},
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
	log.Info("Notifying ComponentBuild Status Update via PR Comment", "name", cb.Name, "scmUrl", cb.Spec.SCMURL, "PRURL", cb.Spec.PRURL, "state", cb.Status.State)
	return r.client.Create(ctx, tr)
}

func (r *ReconcileArtifactBuild) deployArtifact(ctx context.Context, log logr.Logger, abr *jvmbs.ArtifactBuild) error {
	// TODO: We should throttle the creation of deploy tasks so we dont swamp the cluster
	// We also need to review the relationship between deploy tasks, dependencybuilds and rebuiltartifacts
	db := r.getDependencyBuild(ctx, log, abr)
	if db != nil && db.Annotations["io.aphelia/deployed"] == "" {
		existing := v1beta1.TaskRunList{}
		listOpts := &client.ListOptions{
			Namespace:     abr.Namespace,
			LabelSelector: labels.SelectorFromSet(map[string]string{DeployTaskLabel: db.Name}),
		}
		err := r.client.List(ctx, &existing, listOpts)
		if err != nil {
			return err
		}
		for _, i := range existing.Items {
			if i.Status.GetCondition(apis.ConditionSucceeded).IsUnknown() {
				return nil
			}
		}
		tr := &v1beta1.TaskRun{}
		tr.GenerateName = abr.Name + "-deploy-task"
		tr.Namespace = abr.Namespace
		orerr := controllerutil.SetOwnerReference(abr, tr, r.scheme)
		if orerr != nil {
			log.Error(orerr, fmt.Sprintf("Error handling taskrun %s", tr.Name))
		}
		tr.Labels = map[string]string{DeployTaskLabel: db.Name}
		tr.Spec.TaskRef = &v1beta1.TaskRef{Name: "apheleia-deploy", Kind: v1beta1.ClusterTaskKind}
		tr.Spec.Params = []v1beta1.Param{
			{Name: "DOMAIN", Value: v1beta1.ArrayOrString{StringVal: "rhosak", Type: v1beta1.ParamTypeString}},
			{Name: "OWNER", Value: v1beta1.ArrayOrString{StringVal: "237843776254", Type: v1beta1.ParamTypeString}},
			{Name: "REPO", Value: v1beta1.ArrayOrString{StringVal: "https://rhosak-237843776254.d.codeartifact.us-east-2.amazonaws.com/maven/sdouglas-scratch/", Type: v1beta1.ParamTypeString}},
			{Name: "FORCE", Value: v1beta1.ArrayOrString{StringVal: "false", Type: v1beta1.ParamTypeString}},
			{Name: "ARTIFACT", Value: v1beta1.ArrayOrString{StringVal: abr.Name, Type: v1beta1.ParamTypeString}},
		}
		return r.client.Create(ctx, tr)
	}
	return nil
}
func (r *ReconcileArtifactBuild) handleArtifactBuildReceived(ctx context.Context, log logr.Logger, abr *jvmbs.ArtifactBuild) (reconcile.Result, error) {
	log.Info("Handling ArtifactBuild", "name", abr.Name, "state", abr.Status.State)
	cbList := v1alpha1.ComponentBuildList{}
	err := r.client.List(ctx, &cbList, client.InNamespace(abr.Namespace))
	if err != nil {
		return reconcile.Result{}, err
	}
	for _, i := range cbList.Items {
		_, exists := i.Status.ArtifactState[abr.Spec.GAV]
		if exists {
			cbItem := i
			_, cberr := r.handleComponentBuildReceived(ctx, log, &cbItem)
			if cberr != nil {
				log.Error(cberr, fmt.Sprintf("Error handling componentbuild %s", i.Name))
			}
		}
	}
	return reconcile.Result{}, nil
}

func (r *ReconcileArtifactBuild) handleTaskRunReceived(ctx context.Context, log logr.Logger, tr *v1beta1.TaskRun) (reconcile.Result, error) {
	log.Info("Handling TaskRun", "name", tr.Name)
	if tr.Status.CompletionTime == nil || tr.Labels["tekton.dev/clusterTask"] != "apheleia-deploy" {
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
		if strings.EqualFold(ownerRef.Kind, "artifactbuild") || strings.EqualFold(ownerRef.Kind, "artifactbuilds") {
			ownerName = ownerRef.Name
			break
		}
	}
	if len(ownerName) == 0 {
		msg := "taskrun missing artifactbuild ownerrefs %s:%s"
		r.eventRecorder.Eventf(tr, v1.EventTypeWarning, "MissingOwner", msg, tr.Namespace, tr.Name)
		log.Info(msg, tr.Namespace, tr.Name)
		return reconcile.Result{}, nil
	}

	key := types.NamespacedName{Namespace: tr.Namespace, Name: ownerName}
	ab := jvmbs.ArtifactBuild{}
	err := r.client.Get(ctx, key, &ab)
	if err != nil {
		msg := "get for taskrun %s:%s owning artifact build %s:%s yielded error %s"
		r.eventRecorder.Eventf(tr, v1.EventTypeWarning, msg, tr.Namespace, tr.Name, tr.Namespace, ownerName, err.Error())
		log.Error(err, fmt.Sprintf(msg, tr.Namespace, tr.Name, tr.Namespace, ownerName, err.Error()))
		return reconcile.Result{}, err
	}
	if tr.Status.GetCondition(apis.ConditionSucceeded).IsTrue() {
		db := r.getDependencyBuild(ctx, log, &ab)
		if db != nil {
			if db.Annotations == nil {
				db.Annotations = map[string]string{}
			}
			db.Annotations["io.aphelia/deployed"] = "true"
			err := r.client.Update(ctx, db)
			if err != nil {
				log.Error(err, fmt.Sprintf("Error updating dependency build with deploy annotation %s", db.Name))
			}
		}
	}
	return r.handleArtifactBuildReceived(ctx, log, &ab)
}

func (r *ReconcileArtifactBuild) handlePipelineRunReceived(ctx context.Context, log logr.Logger, pr *v1beta1.PipelineRun) (reconcile.Result, error) {
	log.Info("Handling PipelineRun", "name", pr.Name)
	if pr.Labels["tekton.dev/pipeline"] != "component-build-notifier" {
		return reconcile.Result{}, nil
	}
	if pr.Status.CompletionTime == nil {
		return reconcile.Result{}, nil
	}
	ownerRefs := pr.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		msg := "pipelinerun missing onwerrefs %s:%s"
		r.eventRecorder.Eventf(pr, v1.EventTypeWarning, msg, pr.Namespace, pr.Name)
		log.Info(msg, pr.Namespace, pr.Name)
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
		msg := "pipelinerun missing componentbuild ownerrefs %s:%s"
		r.eventRecorder.Eventf(pr, v1.EventTypeWarning, "MissingOwner", msg, pr.Namespace, pr.Name)
		log.Info(msg, pr.Namespace, pr.Name)
		return reconcile.Result{}, nil
	}

	key := types.NamespacedName{Namespace: pr.Namespace, Name: ownerName}
	cb := v1alpha1.ComponentBuild{}
	err := r.client.Get(ctx, key, &cb)
	if err != nil {
		msg := "get for pipelinerun %s:%s owning component build %s:%s yielded error %s"
		r.eventRecorder.Eventf(pr, v1.EventTypeWarning, msg, pr.Namespace, pr.Name, pr.Namespace, ownerName, err.Error())
		log.Error(err, fmt.Sprintf(msg, pr.Namespace, pr.Name, pr.Namespace, ownerName, err.Error()))
		return reconcile.Result{}, err
	}
	if pr.Status.GetCondition(apis.ConditionSucceeded).IsTrue() {
		cb.Status.ResultNotified = true
		log.Info("Setting resultNotified: True for ComponentBuild Status", "name", cb.Name)
		return reconcile.Result{}, r.client.Status().Update(ctx, &cb)
	}
	return r.handleComponentBuildReceived(ctx, log, &cb)
}

func (r *ReconcileArtifactBuild) artifactState(ctx context.Context, log logr.Logger, abr *jvmbs.ArtifactBuild) v1alpha1.ArtifactState {
	failed := abr.Status.State == jvmbs.ArtifactBuildStateFailed || abr.Status.State == jvmbs.ArtifactBuildStateMissing
	built := abr.Status.State == jvmbs.ArtifactBuildStateComplete
	deployed := false
	if built {
		db := r.getDependencyBuild(ctx, log, abr)
		if db != nil && db.Annotations["io.aphelia/deployed"] == "true" {
			deployed = true
		}
	}
	return v1alpha1.ArtifactState{ArtifactBuild: abr.Name, Failed: failed, Built: built, Deployed: deployed}
}

func (r *ReconcileArtifactBuild) getDependencyBuild(ctx context.Context, log logr.Logger, abr *jvmbs.ArtifactBuild) *jvmbs.DependencyBuild {
	key := types.NamespacedName{Namespace: abr.Namespace, Name: abr.Name}
	ra := jvmbs.RebuiltArtifact{}
	err := r.client.Get(ctx, key, &ra)
	if err != nil {
		return nil
	}
	ownerReferences := ra.GetOwnerReferences()
	depenencyBuildName := ""
	for _, ownerReference := range ownerReferences {
		if ownerReference.Kind == "DependencyBuild" {
			depenencyBuildName = ownerReference.Name
		}
	}
	if depenencyBuildName != "" {
		key := types.NamespacedName{Namespace: abr.Namespace, Name: depenencyBuildName}
		da := jvmbs.DependencyBuild{}
		err := r.client.Get(ctx, key, &da)
		if err == nil {
			return &da
		}
	}
	return nil
}
