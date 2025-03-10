/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	certmanagerV1 "github.com/jetstack/cert-manager/pkg/apis/certmanager/v1"
	servicecatalogv1beta1 "github.com/kubernetes-sigs/service-catalog/pkg/apis/servicecatalog/v1beta1"
	clusterV1alpha1 "github.com/tmax-cloud/hypercloud-multi-operator/apis/cluster/v1alpha1"
	util "github.com/tmax-cloud/hypercloud-multi-operator/controllers/util"
	traefikV1alpha1 "github.com/traefik/traefik/v2/pkg/provider/kubernetes/crd/traefik/v1alpha1"

	coreV1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"

	capiV1alpha3 "sigs.k8s.io/cluster-api/api/v1alpha3"
	controlplanev1 "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	requeueAfter10Second = 10 * time.Second
	requeueAfter20Second = 20 * time.Second
	requeueAfter30Second = 30 * time.Second
	requeueAfter1Minute  = 1 * time.Minute
)

type ClusterParameter struct {
	Namespace         string
	ClusterName       string
	MasterNum         int
	WorkerNum         int
	Owner             string
	KubernetesVersion string
}

type AwsParameter struct {
	SshKey     string
	Region     string
	MasterType string
	WorkerType string
}

type VsphereParameter struct {
	PodCidr             string
	VcenterIp           string
	VcenterId           string
	VcenterPassword     string
	VcenterThumbprint   string
	VcenterNetwork      string
	VcenterDataCenter   string
	VcenterDataStore    string
	VcenterFolder       string
	VcenterResourcePool string
	VcenterKcpIp        string
	VcenterCpuNum       int
	VcenterMemSize      int
	VcenterDiskSize     int
	VcenterTemplate     string
}

// ClusterManagerReconciler reconciles a ClusterManager object
type ClusterManagerReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cluster.tmax.io,resources=clustermanagers,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=cluster.tmax.io,resources=clustermanagers/status,verbs=get;list;patch;update;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinedeployments/status,verbs=get;list;patch;update;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings;roles;rolebindings,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kubeadmcontrolplanes,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kubeadmcontrolplanes/status,verbs=get;list;patch;update;watch
// +kubebuilder:rbac:groups=servicecatalog.k8s.io,resources=serviceinstances,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=servicecatalog.k8s.io,resources=serviceinstances/status,verbs=get;list;patch;update;watch
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=services;endpoints,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=traefik.containo.us,resources=middlewares,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=create;delete;get;list;patch;update;watch

func (r *ClusterManagerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	_ = context.Background()
	log := r.Log.WithValues("clustermanager", req.NamespacedName)

	//get ClusterManager
	clusterManager := &clusterV1alpha1.ClusterManager{}
	if err := r.Get(context.TODO(), req.NamespacedName, clusterManager); errors.IsNotFound(err) {
		log.Info("ClusterManager resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get ClusterManager")
		return ctrl.Result{}, err
	}

	//set patch helper
	patchHelper, err := patch.NewHelper(clusterManager, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	defer func() {
		// Always reconcile the Status.Phase field.
		r.reconcilePhase(context.TODO(), clusterManager)

		if err := patchHelper.Patch(context.TODO(), clusterManager); err != nil {
			// if err := patchClusterManager(context.TODO(), patchHelper, clusterManager, patchOpts...); err != nil {
			// reterr = kerrors.NewAggregate([]error{reterr, err})
			reterr = err
		}
	}()

	// Add finalizer first if not exist to avoid the race condition between init and delete
	if !controllerutil.ContainsFinalizer(clusterManager, clusterV1alpha1.ClusterManagerFinalizer) {
		controllerutil.AddFinalizer(clusterManager, clusterV1alpha1.ClusterManagerFinalizer)
		return ctrl.Result{}, nil
	}

	// Label migration for old version
	if _, ok := clusterManager.GetLabels()[clusterV1alpha1.LabelKeyClmClusterTypeDefunct]; ok {
		clusterManager.Labels[clusterV1alpha1.LabelKeyClmClusterType] =
			clusterManager.Labels[clusterV1alpha1.LabelKeyClmClusterTypeDefunct]
	}

	if clusterManager.Labels[clusterV1alpha1.LabelKeyClmClusterType] == clusterV1alpha1.ClusterTypeRegistered {
		// Handle deletion reconciliation loop.
		if !clusterManager.ObjectMeta.DeletionTimestamp.IsZero() {
			clusterManager.Status.Ready = false
			return r.reconcileDeleteForRegisteredClusterManager(context.TODO(), clusterManager)
		}
		// return r.reconcileForRegisteredClusterManager(context.TODO(), clusterManager)
	} else {
		// Handle deletion reconciliation loop.
		if !clusterManager.ObjectMeta.DeletionTimestamp.IsZero() {
			clusterManager.Status.Ready = false
			return r.reconcileDelete(context.TODO(), clusterManager)
		}
		// return r.reconcile(context.TODO(), clusterManager)
	}

	// Handle normal reconciliation loop.
	return r.reconcile(context.TODO(), clusterManager)
}

// reconcile handles cluster reconciliation.
// func (r *ClusterManagerReconciler) reconcileForRegisteredClusterManager(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
// 	phases := []func(context.Context, *clusterV1alpha1.ClusterManager) (ctrl.Result, error){
// 		r.UpdateClusterManagerStatus,
// 		r.CreateTraefikResources,
// 		r.CreateArgocdClusterSecret,
// 		r.CreateMonitoringResources,
// 		r.CreateHyperauthClient,
// 		r.SetHyperregistryOidcConfig,
// 	}

// 	res := ctrl.Result{}
// 	errs := []error{}
// 	for _, phase := range phases {
// 		// Call the inner reconciliation methods.
// 		phaseResult, err := phase(ctx, clusterManager)
// 		if err != nil {
// 			errs = append(errs, err)
// 		}
// 		if len(errs) > 0 {
// 			continue
// 		}
// 		res = util.LowestNonZeroResult(res, phaseResult)
// 	}
// 	return res, kerrors.NewAggregate(errs)
// }

func (r *ClusterManagerReconciler) reconcileDeleteForRegisteredClusterManager(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (reconcile.Result, error) {
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile delete for registered ClusterManager")

	if err := util.Delete(clusterManager.Namespace, clusterManager.Name); err != nil {
		log.Error(err, "Failed to delete cluster info from cluster_member table")
		return ctrl.Result{}, err
	}

	key := types.NamespacedName{
		Name:      clusterManager.Name + util.KubeconfigSuffix,
		Namespace: clusterManager.Namespace,
	}
	kubeconfigSecret := &coreV1.Secret{}
	if err := r.Get(context.TODO(), key, kubeconfigSecret); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to get kubeconfig Secret")
		return ctrl.Result{}, err
	} else if err == nil {
		if err := r.Delete(context.TODO(), kubeconfigSecret); err != nil {
			log.Error(err, "Failed to delete kubeconfig Secret")
			return ctrl.Result{}, err
		}

		log.Info("Delete kubeconfig Secret successfully")
		// return ctrl.Result{RequeueAfter: requeueAfter10Second}, nil
	}

	if err := r.DeleteTraefikResources(clusterManager); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.DeleteClientForSingleCluster(clusterManager); err != nil {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(clusterManager, clusterV1alpha1.ClusterManagerFinalizer)
	return ctrl.Result{}, nil
}

// reconcile handles cluster reconciliation.
func (r *ClusterManagerReconciler) reconcile(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
	phases := []func(context.Context, *clusterV1alpha1.ClusterManager) (ctrl.Result, error){}
	if clusterManager.Labels[clusterV1alpha1.LabelKeyClmClusterType] == clusterV1alpha1.ClusterTypeCreated {
		phases = append(
			phases,
			r.CreateServiceInstance,
			r.SetEndpoint,
			r.kubeadmControlPlaneUpdate,
			r.machineDeploymentUpdate,
		)
	} else {
		phases = append(phases, r.UpdateClusterManagerStatus)
	}
	phases = append(
		phases,
		r.CreateTraefikResources,
		r.CreateArgocdClusterSecret,
		r.CreateMonitoringResources,
		r.CreateHyperauthClient,
		r.SetHyperregistryOidcConfig,
	)

	res := ctrl.Result{}
	errs := []error{}
	for _, phase := range phases {
		// Call the inner reconciliation methods.
		phaseResult, err := phase(ctx, clusterManager)
		if err != nil {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			continue
		}

		res = util.LowestNonZeroResult(res, phaseResult)
	}

	return res, kerrors.NewAggregate(errs)
}

func (r *ClusterManagerReconciler) reconcileDelete(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (reconcile.Result, error) {
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start reconcile phase for delete")

	key := types.NamespacedName{
		Name:      clusterManager.Name + util.KubeconfigSuffix,
		Namespace: clusterManager.Namespace,
	}
	kubeconfigSecret := &coreV1.Secret{}
	if err := r.Get(context.TODO(), key, kubeconfigSecret); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to get kubeconfig secret")
		return ctrl.Result{}, err
	}

	if err := r.DeleteTraefikResources(clusterManager); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.DeleteClientForSingleCluster(clusterManager); err != nil {
		return ctrl.Result{}, err
	}

	// remoteClientset, err := util.GetRemoteK8sClient(kubeconfigSecret)
	// if err != nil {
	// 	log.Error(err, "Failed to get remoteK8sClient")
	// 	return ctrl.Result{}, err
	// }

	// secret은 존재하는데.. 실제 instance가 없어서 에러 발생
	// _, err = remoteClientset.
	// 	CoreV1().
	// 	Namespaces().
	// 	Get(context.TODO(), util.IngressNginxNamespace, metav1.GetOptions{})
	// if errors.IsNotFound(err) {
	// 	log.Info("Ingress-nginx namespace is already deleted")
	// } else if err != nil {
	// 	log.Info(err.Error())
	// 	log.Info("Failed to get Ingress-nginx loadbalancer service... may be instance was deleted before secret was deleted...")
	// 	// log.Info("###################### Never executed... ############################")
	// 	// error 처리 필요
	// } else {
	// 	err := remoteClientset.
	// 		CoreV1().
	// 		Namespaces().
	// 		Delete(context.TODO(), util.IngressNginxNamespace, metav1.DeleteOptions{})
	// 	if err != nil {
	// 		log.Error(err, "Failed to delete Ingress-nginx namespace")
	// 		return ctrl.Result{}, err
	// 	}
	// }

	// delete serviceinstance
	key = types.NamespacedName{
		Name:      clusterManager.Name + "-" + clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmSuffix],
		Namespace: clusterManager.Namespace,
	}
	serviceInstance := &servicecatalogv1beta1.ServiceInstance{}
	if err := r.Get(context.TODO(), key, serviceInstance); errors.IsNotFound(err) {
		log.Info("ServiceInstance is already deleted. Waiting cluster to be deleted")
	} else if err != nil {
		log.Error(err, "Failed to get serviceInstance")
		return ctrl.Result{}, err
	} else {
		if err := r.Delete(context.TODO(), serviceInstance); err != nil {
			log.Error(err, "Failed to delete serviceInstance")
			return ctrl.Result{}, err
		}
	}

	//delete handling
	key = clusterManager.GetNamespacedName()
	if err := r.Get(context.TODO(), key, &capiV1alpha3.Cluster{}); errors.IsNotFound(err) {
		if err := util.Delete(clusterManager.Namespace, clusterManager.Name); err != nil {
			log.Error(err, "Failed to delete cluster info from cluster_member table")
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(clusterManager, clusterV1alpha1.ClusterManagerFinalizer)
		return ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get cluster")
		return ctrl.Result{}, err
	}

	log.Info("Cluster is deleteing. Requeue after 1min")
	return ctrl.Result{RequeueAfter: requeueAfter1Minute}, nil
}

func (r *ClusterManagerReconciler) reconcilePhase(_ context.Context, clusterManager *clusterV1alpha1.ClusterManager) {
	if clusterManager.Status.Phase == "" {
		if clusterManager.Labels[clusterV1alpha1.LabelKeyClmClusterType] == clusterV1alpha1.ClusterTypeRegistered {
			clusterManager.Status.SetTypedPhase(clusterV1alpha1.ClusterManagerPhaseRegistering)
		} else {
			clusterManager.Status.SetTypedPhase(clusterV1alpha1.ClusterManagerPhaseProvisioning)
		}
	}

	if clusterManager.Status.Ready {
		if clusterManager.Labels[clusterV1alpha1.LabelKeyClmClusterType] == clusterV1alpha1.ClusterTypeRegistered {
			clusterManager.Status.SetTypedPhase(clusterV1alpha1.ClusterManagerPhaseRegistered)
		} else {
			clusterManager.Status.SetTypedPhase(clusterV1alpha1.ClusterManagerPhaseProvisioned)
		}
	}

	if !clusterManager.DeletionTimestamp.IsZero() {
		clusterManager.Status.SetTypedPhase(clusterV1alpha1.ClusterManagerPhaseDeleting)
	}
}

func (r *ClusterManagerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	controller, err := ctrl.NewControllerManagedBy(mgr).
		For(&clusterV1alpha1.ClusterManager{}).
		WithEventFilter(
			predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool {
					return true
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					// created clm은 update 필요가 있지만 registered는 clm update가 필요 없다
					// 다만 registered인 경우 deletiontimestamp가 있는경우 delete 수행을 위해 reconcile을 수행하긴 해야한다.
					oldclm := e.ObjectOld.(*clusterV1alpha1.ClusterManager)
					newclm := e.ObjectNew.(*clusterV1alpha1.ClusterManager)

					isFinalized := !controllerutil.ContainsFinalizer(oldclm, clusterV1alpha1.ClusterManagerFinalizer) &&
						controllerutil.ContainsFinalizer(newclm, clusterV1alpha1.ClusterManagerFinalizer)
					isDelete := oldclm.DeletionTimestamp.IsZero() &&
						!newclm.DeletionTimestamp.IsZero()
					isControlPlaneEndpointUpdate := oldclm.Status.ControlPlaneEndpoint == "" &&
						newclm.Status.ControlPlaneEndpoint != ""
					isSubResourceNotReady := !newclm.Status.ArgoReady ||
						!newclm.Status.TraefikReady ||
						(!newclm.Status.MonitoringReady || !newclm.Status.PrometheusReady)
					if isDelete || isControlPlaneEndpointUpdate || isFinalized {
						return true
					} else {
						if newclm.Labels[clusterV1alpha1.LabelKeyClmClusterType] == clusterV1alpha1.ClusterTypeCreated {
							return true
						}
						if newclm.Labels[clusterV1alpha1.LabelKeyClmClusterType] == clusterV1alpha1.ClusterTypeRegistered {
							return isSubResourceNotReady
						}
					}
					return false
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					return false
				},
				GenericFunc: func(e event.GenericEvent) bool {
					return false
				},
			},
		).
		Build(r)

	if err != nil {
		return err
	}

	controller.Watch(
		&source.Kind{Type: &capiV1alpha3.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterManagersForCluster),
		predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldc := e.ObjectOld.(*capiV1alpha3.Cluster)
				newc := e.ObjectNew.(*capiV1alpha3.Cluster)

				return !oldc.Status.ControlPlaneInitialized && newc.Status.ControlPlaneInitialized
			},
			CreateFunc: func(e event.CreateEvent) bool {
				return false
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return true
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		},
	)

	controller.Watch(
		&source.Kind{Type: &controlplanev1.KubeadmControlPlane{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterManagersForKubeadmControlPlane),
		predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldKcp := e.ObjectOld.(*controlplanev1.KubeadmControlPlane)
				newKcp := e.ObjectNew.(*controlplanev1.KubeadmControlPlane)

				return oldKcp.Status.Replicas != newKcp.Status.Replicas
			},
			CreateFunc: func(e event.CreateEvent) bool {
				return true
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		},
	)

	controller.Watch(
		&source.Kind{Type: &capiV1alpha3.MachineDeployment{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueClusterManagersForMachineDeployment),
		predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldMd := e.ObjectOld.(*capiV1alpha3.MachineDeployment)
				newMd := e.ObjectNew.(*capiV1alpha3.MachineDeployment)

				return oldMd.Status.Replicas != newMd.Status.Replicas
			},
			CreateFunc: func(e event.CreateEvent) bool {
				return true
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		},
	)

	subResources := []client.Object{
		&certmanagerV1.Certificate{},
		&networkingv1.Ingress{},
		&coreV1.Service{},
		// &coreV1.Endpoints{},
		&traefikV1alpha1.Middleware{},
	}
	for _, resource := range subResources {
		controller.Watch(
			&source.Kind{Type: resource},
			handler.EnqueueRequestsFromMapFunc(r.requeueClusterManagersForSubresources),
			predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {
					return false
				},
				CreateFunc: func(e event.CreateEvent) bool {
					return false
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					_, ok := e.Object.GetLabels()[clusterV1alpha1.LabelKeyClmName]
					return ok
				},
				GenericFunc: func(e event.GenericEvent) bool {
					return false
				},
			},
		)
	}

	return nil
}
