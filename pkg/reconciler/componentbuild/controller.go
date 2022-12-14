package componentbuild

import (
	"github.com/stuartwdouglas/apheleia/pkg/apis/apheleia/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/kcp-dev/logicalcluster/v2"
	jvmbs "github.com/redhat-appstudio/jvm-build-service/pkg/apis/jvmbuildservice/v1alpha1"
)

func SetupNewReconcilerWithManager(mgr ctrl.Manager) error {
	r := newReconciler(mgr)
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.ComponentBuild{}).
		Watches(&source.Kind{Type: &jvmbs.ArtifactBuild{}}, handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
			artifactBuild := o.(*jvmbs.ArtifactBuild)
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      artifactBuild.Name,
						Namespace: artifactBuild.Namespace,
					},
					ClusterName: logicalcluster.From(artifactBuild).String(),
				},
			}
		})).Watches(&source.Kind{Type: &jvmbs.ArtifactBuild{}}, handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
		artifactBuild := o.(*jvmbs.ArtifactBuild)
		return []reconcile.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      artifactBuild.Name,
					Namespace: artifactBuild.Namespace,
				},
				ClusterName: logicalcluster.From(artifactBuild).String(),
			},
		}
	})).Watches(&source.Kind{Type: &v1beta1.TaskRun{}}, &handler.EnqueueRequestForOwner{OwnerType: &v1alpha1.ComponentBuild{}, IsController: false}).
		Complete(r)
}
