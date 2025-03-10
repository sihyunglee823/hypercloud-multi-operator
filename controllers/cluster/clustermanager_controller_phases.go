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
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	argocdV1alpha1 "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	servicecatalogv1beta1 "github.com/kubernetes-sigs/service-catalog/pkg/apis/servicecatalog/v1beta1"
	clusterV1alpha1 "github.com/tmax-cloud/hypercloud-multi-operator/apis/cluster/v1alpha1"
	hyperauthCaller "github.com/tmax-cloud/hypercloud-multi-operator/controllers/hyperAuth"
	util "github.com/tmax-cloud/hypercloud-multi-operator/controllers/util"

	coreV1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"

	capiV1alpha3 "sigs.k8s.io/cluster-api/api/v1alpha3"
	controlplanev1 "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"
)

func (r *ClusterManagerReconciler) UpdateClusterManagerStatus(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
	if clusterManager.Status.ControlPlaneReady {
		return ctrl.Result{}, nil
	}
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for UpdateClusterManagerStatus")

	kubeconfigSecret, err := r.GetKubeconfigSecret(clusterManager)
	if err != nil {
		log.Error(err, "Failed to get kubeconfig secret")
		return ctrl.Result{RequeueAfter: requeueAfter10Second}, nil
	}

	remoteClientset, err := util.GetRemoteK8sClient(kubeconfigSecret)
	if err != nil {
		log.Error(err, "Failed to get remoteK8sClient")
		return ctrl.Result{}, err
	}

	kubeadmConfig, err := remoteClientset.
		CoreV1().
		ConfigMaps(util.KubeNamespace).
		Get(context.TODO(), "kubeadm-config", metav1.GetOptions{})
	if err != nil {
		log.Error(err, "Failed to get kubeadm-config configmap from remote cluster")
		return ctrl.Result{}, err
	}

	jsonData, _ := yaml.YAMLToJSON([]byte(kubeadmConfig.Data["ClusterConfiguration"]))
	data := make(map[string]interface{})
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return ctrl.Result{}, err
	}
	clusterManager.Spec.Version = fmt.Sprintf("%v", data["kubernetesVersion"])

	nodeList, err := remoteClientset.
		CoreV1().
		Nodes().
		List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Error(err, "Failed to list remote K8s nodeList")
		return ctrl.Result{}, err
	}

	// var machineList *capiV1alpha3.machineList
	// if machineList, err =
	// todo - shkim
	// node list가 아닌 machine list를 불러서 ready체크를 해야 확실하지 않을까?
	clusterManager.Spec.MasterNum = 0
	clusterManager.Status.MasterRun = 0
	clusterManager.Spec.WorkerNum = 0
	clusterManager.Status.WorkerRun = 0
	clusterManager.Spec.Provider = util.ProviderUnknown
	clusterManager.Status.Provider = util.ProviderUnknown
	for _, node := range nodeList.Items {
		if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
			clusterManager.Spec.MasterNum++
			if node.Status.Conditions[len(node.Status.Conditions)-1].Type == "Ready" {
				clusterManager.Status.MasterRun++
			}
		} else {
			clusterManager.Spec.WorkerNum++
			if node.Status.Conditions[len(node.Status.Conditions)-1].Type == "Ready" {
				clusterManager.Status.WorkerRun++
			}
		}

		if clusterManager.Spec.Provider == util.ProviderUnknown && node.Spec.ProviderID != "" {
			providerID, err := util.GetProviderName(
				strings.Split(node.Spec.ProviderID, "://")[0],
			)
			if err != nil {
				log.Error(err, "Cannot found given provider name.")
			}
			clusterManager.Status.Provider = providerID
			clusterManager.Spec.Provider = providerID
		}
	}

	if clusterManager.Spec.Provider == util.ProviderUnknown {
		reg, _ := regexp.Compile(`cloud-provider: [a-zA-Z-_ ]+`)
		matchString := reg.FindString(kubeadmConfig.Data["ClusterConfiguration"])
		if matchString != "" {
			cloudProvider, err := util.GetProviderName(
				matchString[len("cloud-provider: "):],
			)
			if err != nil {
				log.Error(err, "Cannot found given provider name.")
			}
			clusterManager.Status.Provider = cloudProvider
			clusterManager.Spec.Provider = cloudProvider
		}
	}

	// health check
	resp, err := remoteClientset.
		RESTClient().
		Get().
		AbsPath("/readyz").
		DoRaw(context.TODO())
	if err != nil {
		log.Error(err, "Failed to get remote cluster status")
		return ctrl.Result{}, err
	}
	if string(resp) == "ok" {
		clusterManager.Status.ControlPlaneReady = true
		//clusterManager.Status.AgentReady = true
		clusterManager.Status.Ready = true
	} else {
		// err := errors.NewBadRequest("Failed to healthcheck")
		// log.Error(err, "Failed to healthcheck")
		log.Info("Remote cluster is not ready... wait...")
		return ctrl.Result{RequeueAfter: requeueAfter30Second}, nil
	}

	log.Info("Update status of ClusterManager successfully")
	generatedSuffix := util.CreateSuffixString()
	clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmSuffix] = generatedSuffix
	return ctrl.Result{}, nil
}

// defunct
// func (r *ClusterManagerReconciler) DeployAndUpdateAgentEndpoint(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
// 	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
// 	log.Info("Start to reconcile phase for DeployAndUpdateAgentEndpoint")

// 	// secret controller에서 clustermanager.status.controleplaneendpoint를 채워줄 때 까지 기다림
// 	if !clusterManager.Status.ControlPlaneReady {
// 		return ctrl.Result{RequeueAfter: requeueAfter1Minute}, nil
// 	}

// 	kubeconfigSecret := &coreV1.Secret{}
// 	key := types.NamespacedName{
// 		Name:      clusterManager.Name + util.KubeconfigSuffix,
// 		Namespace: clusterManager.Namespace,
// 	}
// 	if err := r.Get(context.TODO(), key, kubeconfigSecret); errors.IsNotFound(err) {
// 		log.Info("Wait for creating kubeconfig secret.")
// 		return ctrl.Result{RequeueAfter: requeueAfter10Second}, nil
// 	} else if err != nil {
// 		log.Error(err, "Failed to get kubeconfig secret")
// 		return ctrl.Result{}, err
// 	}

// 	remoteClientset, err := util.GetRemoteK8sClient(kubeconfigSecret)
// 	if err != nil {
// 		log.Error(err, "Failed to get remoteK8sClient")
// 		return ctrl.Result{}, err
// 	}

// 	// ingress controller 존재하는지 먼저 확인하고 없으면 배포부터해.. 그전에 join되었는지도 먼저 확인해야하나...
// 	_, err = remoteClientset.
// 		CoreV1().
// 		Namespaces().
// 		Get(context.TODO(), util.IngressNginxNamespace, metav1.GetOptions{})
// 	if errors.IsNotFound(err) {
// 		log.Info("Cannot found ingress namespace. Ingress-nginx is creating. Requeue after 30sec")
// 		return ctrl.Result{RequeueAfter: requeueAfter1Minute}, nil
// 	} else if err != nil {
// 		log.Error(err, "Failed to get ingress-nginx namespace from remote cluster")
// 		return ctrl.Result{}, err
// 	} else {
// 		ingressController, err := remoteClientset.
// 			AppsV1().
// 			Deployments(util.IngressNginxNamespace).
// 			Get(context.TODO(), util.IngressNginxName, metav1.GetOptions{})
// 		if errors.IsNotFound(err) {
// 			log.Info("Cannot found ingress controller. Ingress-nginx is creating. Requeue after 30sec")
// 			return ctrl.Result{RequeueAfter: requeueAfter1Minute}, nil
// 		} else if err != nil {
// 			log.Error(err, "Failed to get ingress controller from remote cluster")
// 			return ctrl.Result{}, err
// 		} else {
// 			// 하나라도 ready라면..
// 			if ingressController.Status.ReadyReplicas == 0 {
// 				log.Info("Ingress controller is not ready. Requeue after 60sec")
// 				return ctrl.Result{RequeueAfter: requeueAfter1Minute}, nil
// 			}
// 		}
// 	}

// 	clusterManager.Status.Ready = true
// 	return ctrl.Result{}, nil
// }

func (r *ClusterManagerReconciler) CreateServiceInstance(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
	if clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmSuffix] != "" {
		return ctrl.Result{}, nil
	}
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for CreateServiceInstance")

	key := types.NamespacedName{
		Name:      clusterManager.Name + clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmSuffix],
		Namespace: clusterManager.Namespace,
	}
	if err := r.Get(context.TODO(), key, &servicecatalogv1beta1.ServiceInstance{}); errors.IsNotFound(err) {
		clusterJson, err := json.Marshal(
			&ClusterParameter{
				Namespace:         clusterManager.Namespace,
				ClusterName:       clusterManager.Name,
				Owner:             clusterManager.Annotations[util.AnnotationKeyOwner],
				KubernetesVersion: clusterManager.Spec.Version,
				MasterNum:         clusterManager.Spec.MasterNum,
				WorkerNum:         clusterManager.Spec.WorkerNum,
			},
		)
		if err != nil {
			log.Error(err, "Failed to marshal cluster parameters")
		}

		var providerJson []byte
		switch strings.ToUpper(clusterManager.Spec.Provider) {
		case util.ProviderAws:
			providerJson, err = json.Marshal(
				&AwsParameter{
					SshKey:     clusterManager.AwsSpec.SshKey,
					Region:     clusterManager.AwsSpec.Region,
					MasterType: clusterManager.AwsSpec.MasterType,
					WorkerType: clusterManager.AwsSpec.WorkerType,
				},
			)
			if err != nil {
				log.Error(err, "Failed to marshal cluster parameters")
				return ctrl.Result{}, err
			}
		case util.ProviderVsphere:
			providerJson, err = json.Marshal(
				&VsphereParameter{
					PodCidr:             clusterManager.VsphereSpec.PodCidr,
					VcenterIp:           clusterManager.VsphereSpec.VcenterIp,
					VcenterId:           clusterManager.VsphereSpec.VcenterId,
					VcenterPassword:     clusterManager.VsphereSpec.VcenterPassword,
					VcenterThumbprint:   clusterManager.VsphereSpec.VcenterThumbprint,
					VcenterNetwork:      clusterManager.VsphereSpec.VcenterNetwork,
					VcenterDataCenter:   clusterManager.VsphereSpec.VcenterDataCenter,
					VcenterDataStore:    clusterManager.VsphereSpec.VcenterDataStore,
					VcenterFolder:       clusterManager.VsphereSpec.VcenterFolder,
					VcenterResourcePool: clusterManager.VsphereSpec.VcenterResourcePool,
					VcenterKcpIp:        clusterManager.VsphereSpec.VcenterKcpIp,
					VcenterCpuNum:       clusterManager.VsphereSpec.VcenterCpuNum,
					VcenterMemSize:      clusterManager.VsphereSpec.VcenterMemSize,
					VcenterDiskSize:     clusterManager.VsphereSpec.VcenterDiskSize,
					VcenterTemplate:     clusterManager.VsphereSpec.VcenterTemplate,
				},
			)
			if err != nil {
				log.Error(err, "Failed to marshal cluster parameters")
				return ctrl.Result{}, err
			}
		}

		clusterJson = util.MergeJson(clusterJson, providerJson)
		generatedSuffix := util.CreateSuffixString()
		serviceInstance := &servicecatalogv1beta1.ServiceInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterManager.Name + "-" + generatedSuffix,
				Namespace: clusterManager.Namespace,
				Annotations: map[string]string{
					util.AnnotationKeyOwner:   clusterManager.Annotations[util.AnnotationKeyCreator],
					util.AnnotationKeyCreator: clusterManager.Annotations[util.AnnotationKeyCreator],
				},
			},
			Spec: servicecatalogv1beta1.ServiceInstanceSpec{
				PlanReference: servicecatalogv1beta1.PlanReference{
					ClusterServiceClassExternalName: "capi-" + strings.ToLower(clusterManager.Spec.Provider) + "-template",
					ClusterServicePlanExternalName:  "capi-" + strings.ToLower(clusterManager.Spec.Provider) + "-template-plan-default",
				},
				Parameters: &runtime.RawExtension{
					Raw: clusterJson,
				},
			},
		}
		if err = r.Create(context.TODO(), serviceInstance); err != nil {
			log.Error(err, "Failed to create ServiceInstance")
			return ctrl.Result{}, err
		}

		ctrl.SetControllerReference(clusterManager, serviceInstance, r.Scheme)
		clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmSuffix] = generatedSuffix
	} else if err != nil {
		log.Error(err, "Failed to get ServiceInstance")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ClusterManagerReconciler) SetEndpoint(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
	if clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmApiserver] != "" {
		return ctrl.Result{}, nil
	}
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for SetEndpoint")

	key := clusterManager.GetNamespacedName()
	cluster := &capiV1alpha3.Cluster{}
	if err := r.Get(context.TODO(), key, cluster); errors.IsNotFound(err) {
		log.Info("Failed to get cluster. Requeue after 20sec")
		return ctrl.Result{RequeueAfter: requeueAfter20Second}, err
	} else if err != nil {
		log.Error(err, "Failed to get cluster")
		return ctrl.Result{}, err
	}

	if cluster.Spec.ControlPlaneEndpoint.Host == "" {
		log.Info("ControlPlain endpoint is not ready yet. requeue after 20sec")
		return ctrl.Result{RequeueAfter: requeueAfter20Second}, nil
	}
	clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmApiserver] = cluster.Spec.ControlPlaneEndpoint.Host

	return ctrl.Result{}, nil
}

func (r *ClusterManagerReconciler) kubeadmControlPlaneUpdate(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for kubeadmControlPlaneUpdate")

	key := types.NamespacedName{
		Name:      clusterManager.Name + "-control-plane",
		Namespace: clusterManager.Namespace,
	}
	kcp := &controlplanev1.KubeadmControlPlane{}
	if err := r.Get(context.TODO(), key, kcp); errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get clusterRole")
		return ctrl.Result{}, err
	}

	//create helper for patch
	helper, _ := patch.NewHelper(kcp, r.Client)
	defer func() {
		if err := helper.Patch(context.TODO(), kcp); err != nil {
			r.Log.Error(err, "KubeadmControlPlane patch error")
		}
	}()

	if *kcp.Spec.Replicas != int32(clusterManager.Spec.MasterNum) {
		*kcp.Spec.Replicas = int32(clusterManager.Spec.MasterNum)
	}

	if kcp.Spec.Version != clusterManager.Spec.Version {
		kcp.Spec.Version = clusterManager.Spec.Version
	}

	clusterManager.Status.Ready = true
	return ctrl.Result{}, nil
}

func (r *ClusterManagerReconciler) machineDeploymentUpdate(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for machineDeploymentUpdate")

	key := types.NamespacedName{
		Name:      clusterManager.Name + "-md-0",
		Namespace: clusterManager.Namespace,
	}
	md := &capiV1alpha3.MachineDeployment{}
	if err := r.Get(context.TODO(), key, md); errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	} else if err != nil {
		log.Error(err, "Failed to get clusterRole")
		return ctrl.Result{}, err
	}

	//create helper for patch
	helper, _ := patch.NewHelper(md, r.Client)
	defer func() {
		if err := helper.Patch(context.TODO(), md); err != nil {
			r.Log.Error(err, "kubeadmcontrolplane patch error")
		}
	}()

	if *md.Spec.Replicas != int32(clusterManager.Spec.WorkerNum) {
		*md.Spec.Replicas = int32(clusterManager.Spec.WorkerNum)
	}

	if *md.Spec.Template.Spec.Version != clusterManager.Spec.Version {
		*md.Spec.Template.Spec.Version = clusterManager.Spec.Version
	}

	return ctrl.Result{}, nil
}

func (r *ClusterManagerReconciler) CreateTraefikResources(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
	if !clusterManager.Status.Ready || clusterManager.Status.TraefikReady {
		return ctrl.Result{}, nil
	}
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for CreateTraefikResources")

	if err := r.CreateCertificate(clusterManager); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.CreateIngress(clusterManager); err != nil {
		return ctrl.Result{}, err
	}

	// if err := r.CreateService(clusterManager); err != nil {
	// 	return ctrl.Result{}, err
	// }

	if err := r.CreateMiddleware(clusterManager); err != nil {
		return ctrl.Result{}, err
	}

	// ip address도 kube service의 externalName에 들어갈 수 있으므로 logic을 분리할 필요가 없다!
	// if !util.IsIpAddress(clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmApiserver]) {
	// 	clusterManager.Status.TraefikReady = true
	// 	return ctrl.Result{}, nil
	// }

	// if err := r.CreateEndpoint(clusterManager); err != nil {
	// 	return ctrl.Result{}, err
	// }

	clusterManager.Status.TraefikReady = true
	return ctrl.Result{}, nil
}

func (r *ClusterManagerReconciler) CreateArgocdClusterSecret(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (ctrl.Result, error) {
	if !clusterManager.Status.TraefikReady || clusterManager.Status.ArgoReady {
		return ctrl.Result{}, nil
	}
	log := r.Log.WithValues("ClusterManager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for CreateArgocdClusterSecret")

	kubeconfigSecret, err := r.GetKubeconfigSecret(clusterManager)
	if err != nil {
		log.Error(err, "Failed to get kubeconfig secret")
		return ctrl.Result{RequeueAfter: requeueAfter10Second}, nil
	}

	kubeConfig, err := clientcmd.Load(kubeconfigSecret.Data["value"])
	if err != nil {
		log.Error(err, "Failed to get kubeconfig data from secret")
		return ctrl.Result{}, err
	}

	configJson, err := json.Marshal(
		&argocdV1alpha1.ClusterConfig{
			TLSClientConfig: argocdV1alpha1.TLSClientConfig{
				Insecure: false,
				CertData: kubeConfig.AuthInfos[kubeConfig.Contexts[kubeConfig.CurrentContext].AuthInfo].ClientCertificateData,
				KeyData:  kubeConfig.AuthInfos[kubeConfig.Contexts[kubeConfig.CurrentContext].AuthInfo].ClientKeyData,
				CAData:   kubeConfig.Clusters[kubeConfig.Contexts[kubeConfig.CurrentContext].Cluster].CertificateAuthorityData,
			},
		},
	)
	if err != nil {
		log.Error(err, "Failed to marshal cluster authorization parameters")
	}

	clusterName := strings.Split(kubeconfigSecret.Name, util.KubeconfigSuffix)[0]
	key := types.NamespacedName{
		Name:      kubeconfigSecret.Annotations[util.AnnotationKeyArgoClusterSecret],
		Namespace: util.ArgoNamespace,
	}
	argocdClusterSecret := &coreV1.Secret{}
	if err := r.Get(context.TODO(), key, argocdClusterSecret); errors.IsNotFound(err) {
		argocdClusterSecret = &coreV1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kubeconfigSecret.Annotations[util.AnnotationKeyArgoClusterSecret],
				Namespace: util.ArgoNamespace,
				Annotations: map[string]string{
					util.AnnotationKeyOwner:         kubeconfigSecret.Annotations[util.AnnotationKeyOwner],
					util.AnnotationKeyCreator:       kubeconfigSecret.Annotations[util.AnnotationKeyCreator],
					util.AnnotationKeyArgoManagedBy: util.ArgoApiGroup,
				},
				Labels: map[string]string{
					util.LabelKeyClmSecretType:           util.ClmSecretTypeArgo,
					util.LabelKeyArgoSecretType:          util.ArgoSecretTypeCluster,
					clusterV1alpha1.LabelKeyClmName:      clusterManager.Name,
					clusterV1alpha1.LabelKeyClmNamespace: clusterManager.Namespace,
				},
				Finalizers: []string{
					clusterV1alpha1.ClusterManagerFinalizer,
				},
			},
			StringData: map[string]string{
				"config": string(configJson),
				"name":   clusterName,
				"server": kubeConfig.Clusters[kubeConfig.Contexts[kubeConfig.CurrentContext].Cluster].Server,
			},
		}
		if err := r.Create(context.TODO(), argocdClusterSecret); err != nil {
			log.Error(err, "Cannot create Argocd Secret for remote cluster")
			return ctrl.Result{}, err
		}
		log.Info("Create Argocd Secret for remote cluster successfully")
	} else if err != nil {
		log.Error(err, "Failed to get Argocd Secret for remote cluster")
		return ctrl.Result{}, err
	} else if !argocdClusterSecret.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{Requeue: true}, nil
	}

	clusterManager.Status.ArgoReady = true
	return ctrl.Result{}, nil
}

func (r *ClusterManagerReconciler) CreateMonitoringResources(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (reconcile.Result, error) {
	if !clusterManager.Status.ArgoReady ||
		(clusterManager.Status.MonitoringReady && clusterManager.Status.PrometheusReady) {
		return ctrl.Result{}, nil
	}
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for CreateMonitoringResources")

	kubeconfigSecret, err := r.GetKubeconfigSecret(clusterManager)
	if err != nil {
		log.Error(err, "Failed to get kubeconfig secret")
		return ctrl.Result{RequeueAfter: requeueAfter10Second}, nil
	}

	remoteClientset, err := util.GetRemoteK8sClient(kubeconfigSecret)
	if err != nil {
		log.Error(err, "Failed to get remoteK8sClient")
		return ctrl.Result{}, err
	}

	gatewayService, err := remoteClientset.
		CoreV1().
		Services(util.ApiGatewayNamespace).
		Get(context.TODO(), "gateway", metav1.GetOptions{})
	if errors.IsNotFound(err) {
		log.Error(err, "Cannot found Service for gateway. Wait for installing api-gateway. Requeue after 1 min")
		return ctrl.Result{Requeue: true, RequeueAfter: requeueAfter1Minute}, err
	} else if err != nil {
		log.Error(err, "Failed to get Service for gateway")
		return ctrl.Result{}, err
	}

	// nodeport
	// if gatewayService.Spec.Type == coreV1.ServiceTypeNodePort {
	// 	// endpointIP := clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmApiserver]
	// 	if err := r.CreateGatewayService(clusterManager, clusterV1alpha1.AnnotationKeyClmApiserver); err != nil {
	// 		return ctrl.Result{}, err
	// 	}

	// 	clusterManager.Status.MonitoringReady = true
	// 	clusterManager.Status.PrometheusReady = true
	// 	clusterManager.Status.TraefikReady = false
	// 	return ctrl.Result{}, nil
	// }
	annotationKey := clusterV1alpha1.AnnotationKeyClmApiserver
	if gatewayService.Spec.Type != coreV1.ServiceTypeNodePort {
		if gatewayService.Status.LoadBalancer.Ingress == nil {
			err := fmt.Errorf("service for gateway's type is not LoadBalancer or not ready")
			log.Error(err, "Service for api-gateway is not Ready. Requeue after 1 min")
			return ctrl.Result{Requeue: true, RequeueAfter: requeueAfter1Minute}, err
		}

		ingress := gatewayService.Status.LoadBalancer.Ingress[0]
		hostnameOrIp := ingress.Hostname + ingress.IP
		if hostnameOrIp == "" {
			err := fmt.Errorf("service for gateway doesn't have both hostname and ip address")
			log.Error(err, "Service for api-gateway is not Ready. Requeue after 1 min")
			return ctrl.Result{Requeue: true, RequeueAfter: requeueAfter1Minute}, err
		}

		clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmGateway] = hostnameOrIp
		annotationKey = clusterV1alpha1.AnnotationKeyClmGateway
	}

	if err := r.CreateGatewayService(clusterManager, annotationKey); err != nil {
		return ctrl.Result{}, err
	}

	// For migration from b5.0.26.6 > b5.0.26.7
	traefikReady, err := r.DeleteDeprecatedTraefikResources(clusterManager)
	if err != nil {
		return ctrl.Result{}, err
	}
	clusterManager.Status.TraefikReady = traefikReady

	if err := r.DeleteDeprecatedPrometheusResources(clusterManager); err != nil {
		return ctrl.Result{}, err
	}

	// if !util.IsIpAddress(clusterManager.Annotations[clusterV1alpha1.AnnotationKeyClmGateway]) {
	// 	clusterManager.Status.MonitoringReady = true
	// 	clusterManager.Status.PrometheusReady = true
	// 	return ctrl.Result{}, nil
	// }

	// if err := r.CreateGatewayEndpoint(clusterManager); err != nil {
	// 	return ctrl.Result{}, err
	// }

	clusterManager.Status.MonitoringReady = true
	clusterManager.Status.PrometheusReady = true
	return ctrl.Result{}, nil
}

func (r *ClusterManagerReconciler) CreateHyperauthClient(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (reconcile.Result, error) {
	if !clusterManager.Status.MonitoringReady || clusterManager.Status.AuthClientReady {
		return ctrl.Result{}, nil
	}
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for CreateHyperauthClient")

	key := types.NamespacedName{
		Name:      "passwords",
		Namespace: "hyperauth",
	}
	secret := &coreV1.Secret{}
	if err := r.Get(context.TODO(), key, secret); errors.IsNotFound(err) {
		log.Info("Hyperauth password secret is not found")
		return ctrl.Result{}, err
	} else if err != nil {
		log.Error(err, "Failed to get hyperauth password secret")
		return ctrl.Result{}, err
	}

	prefix := clusterManager.Namespace + "-" + clusterManager.Name + "-"
	clientConfigs := hyperauthCaller.GetClientConfigPreset(prefix)
	for _, config := range clientConfigs {
		if err := hyperauthCaller.CreateClient(config, secret); err != nil {
			log.Error(err, "Failed to create hyperauth client ["+config.ClientId+"] for single cluster")
			return ctrl.Result{RequeueAfter: requeueAfter10Second}, err
		}
	}

	protocolMapperMappingConfigs := hyperauthCaller.GetMappingProtocolMapperToClientConfigPreset(prefix)
	for _, config := range protocolMapperMappingConfigs {
		if err := hyperauthCaller.CreateClientLevelProtocolMapper(config, secret); err != nil {
			log.Error(err, "Failed to create hyperauth protocol mapper ["+config.ClientId+"] for single cluster")
			return ctrl.Result{RequeueAfter: requeueAfter10Second}, err
		}
	}

	clientLevelRoleConfigs := hyperauthCaller.GetClientLevelRoleConfigPreset(prefix)
	for _, config := range clientLevelRoleConfigs {
		if err := hyperauthCaller.CreateClientLevelRole(config, secret); err != nil {
			log.Error(err, "Failed to create hyperauth client-level role ["+config.ClientId+"] for single cluster")
			return ctrl.Result{RequeueAfter: requeueAfter10Second}, err
		}

		userEmail := clusterManager.Annotations[util.AnnotationKeyOwner]
		if err := hyperauthCaller.AddClientLevelRolesToUserRoleMapping(config, userEmail, secret); err != nil {
			log.Error(err, "Failed to add client-level role to user role mapping ["+config.ClientId+"] for single cluster")
			return ctrl.Result{RequeueAfter: requeueAfter10Second}, err
		}
	}

	clientScopeMappingConfig := hyperauthCaller.GetClientScopeMappingPreset(prefix)
	for _, config := range clientScopeMappingConfig {
		err := hyperauthCaller.AddClientScopeToClient(config, secret)
		if err != nil {
			log.Error(err, "Failed to add client scope to client ["+config.ClientId+"] for single cluster")
			return ctrl.Result{RequeueAfter: requeueAfter10Second}, err
		}
	}

	log.Info("Create clients for single cluster successfully")
	clusterManager.Status.AuthClientReady = true
	return ctrl.Result{}, nil
}

func (r *ClusterManagerReconciler) SetHyperregistryOidcConfig(ctx context.Context, clusterManager *clusterV1alpha1.ClusterManager) (reconcile.Result, error) {
	if !clusterManager.Status.AuthClientReady || clusterManager.Status.HyperregistryOidcReady {
		return ctrl.Result{}, nil
	}
	log := r.Log.WithValues("clustermanager", clusterManager.GetNamespacedName())
	log.Info("Start to reconcile phase for SetHyperregistryOidcConfig")

	kubeconfigSecret, err := r.GetKubeconfigSecret(clusterManager)
	if err != nil {
		log.Error(err, "Failed to get kubeconfig secret")
		return ctrl.Result{RequeueAfter: requeueAfter10Second}, nil
	}

	remoteClientset, err := util.GetRemoteK8sClient(kubeconfigSecret)
	if err != nil {
		log.Error(err, "Failed to get remoteK8sClient")
		return ctrl.Result{}, err
	}

	secret, err := remoteClientset.
		CoreV1().
		Secrets(util.HyperregistryNamespace).
		Get(context.TODO(), "hyperregistry-harbor-core", metav1.GetOptions{})
	if err != nil {
		log.Error(err, "Failed to get Secret \"hyperregistry-harbor-core\"")
		return ctrl.Result{}, err
	}

	ingress, err := remoteClientset.
		NetworkingV1().
		Ingresses(util.HyperregistryNamespace).
		Get(context.TODO(), "hyperregistry-harbor-ingress", metav1.GetOptions{})
	if err != nil {
		log.Error(err, "Failed to get Ingress \"hyperregistry-harbor-ingress\"")
		return ctrl.Result{}, err
	}

	prefix := clusterManager.Namespace + "-" + clusterManager.Name + "-"
	hyperauthDomain := "https://" + os.Getenv("AUTH_SUBDOMAIN") + "." + os.Getenv("HC_DOMAIN") + "/auth/realms/tmax"
	config := util.OidcConfig{
		AuthMode:         "oidc_auth",
		OidcAdminGroup:   "admin",
		OidcAutoOnBoard:  true,
		OidcClientId:     prefix + "hyperregistry",
		OidcClientSecret: os.Getenv("AUTH_CLIENT_SECRET"),
		OidcEndpoint:     hyperauthDomain,
		OidcGroupsClaim:  "group",
		OidcName:         "hyperregistry",
		OidcScope:        "openid",
		OidcUserClaim:    "preferred_username",
		OidcVerifyCert:   false,
	}
	hostpath := ingress.Spec.Rules[0].Host
	if err := util.SetHyperregistryOIDC(config, secret, hostpath); err != nil {
		log.Error(err, "Failed to get ingress for hyperregistry")
		return ctrl.Result{}, err
	}

	log.Info("Set oidc config for hyperregistry successfully")
	clusterManager.Status.HyperregistryOidcReady = true
	return ctrl.Result{}, nil
}
