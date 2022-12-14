package componentbuild

import (
	"context"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stuartwdouglas/apheleia/pkg/apis/apheleia/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
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
	contextTimeout = 300 * time.Second
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
	if !cb.Status.ResultNotified && (cb.Status.State == v1alpha1.ComponentBuildStateComplete || cb.Status.State == v1alpha1.ComponentBuildStateFailed) {
		err := r.notifyResult(ctx, log, cb)
		if err != nil {
			return reconcile.Result{}, err
		}
		cb.Status.ResultNotified = true
		return reconcile.Result{}, r.client.Status().Update(ctx, cb)
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
