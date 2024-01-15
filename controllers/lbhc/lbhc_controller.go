/*
Copyright 2023.

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

package lbhc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	crossplane "github.com/aluttik/go-crossplane"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	coorv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	lbhcv1 "cloud.edge/lbhc-controller/apis/lbhc/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

var (
	LbHcClient client.Client
	Kubeclient kubernetes.Interface
	Elect      Elector
	//VirtClient kubecli.KubevirtClient
	//KubeovnClient   kubeOvnClient.Interface
	//VmPodPairRecord = make(map[string]*cloudedgev1.FloatingIPSpec) //创建时记录，删除fip资源的时候使用
	ErrBadPattern                                           = errors.New("syntax error in pattern")
	GlobalSyncLoop                                          = 0
	GlobalLbhcLastNotDownStatus                        bool = true
	GlobalRecordEpIpDownDict                           map[string]bool
	GlobalLbhc                                         lbhcv1.Lbhc
	GlobalSvcLeaseDetail                               map[string]string
	GlobalSvcIsLb                                      map[string]bool
	GlobalHostName                                     string
	CrdName, CrdNamespace                              string
	SyncFrequency, WaitSetDownFrequency                time.Duration
	NgxServiceAddress, NgxServicePort, NgxServiceHcUrl string
	DeployIsCluster                                    bool
	GlobalEpCacheInfo                                  map[string]EpCacheInfo
	mutex                                              sync.Mutex
)

const (
	VirtualServiceFullName  = "virtualservices.kubernetes.io/%s_%s"
	VirtualServiceFullValue = "%s_%s"
	VsDomain                = "network.kubernetes.io"
	VsDomainName            = "virtual-service"
	VsAppLabelPrefix        = "virtualservices.kubernetes.io/"
	VsHcIsEnable            = "virtualservices.kubernetes.io/hc_enabled"
	VsHcIsEnableAllPorts    = "virtualservices.kubernetes.io/hc_enabled_allports"
	VsHcProtocolPort        = "virtualservices.kubernetes.io/hc_protocol_port"
	VsHcHttpProtocol        = "virtualservices.kubernetes.io/hc_http_protocol"
	VsHcMonitorParam        = "virtualservices.kubernetes.io/hc_monitor_parameter"
	NginxConfigFile         = "/usr/local/nginx/conf/nginx.conf"
	NgHttpUpstreamFile      = "/usr/local/nginx/conf/conf.d/upstream.conf"
	NgSteamUpstreamFile     = "/usr/local/nginx/conf/conf.d/stream/upstream.conf"
	NgxBin                  = "/usr/local/nginx/sbin/nginx"
	AnnotationFt            = "kubevirt.io/ftvm"
	AnnotationLease         = "network.io/domain"
	VsVipIgnore             = "kube-vip.io/ignore"
	VsKubeVipIpamAddress    = "ipam-address"
)

// LbhcReconciler reconciles a Lbhc object
type LbhcReconciler struct {
	client.Client
	Kubeclient kubernetes.Interface
	Scheme     *runtime.Scheme
}

// LbhcReconciler reconciles a Lbhc object
type LbhcSyncResourceReconciler struct {
	client.Client
	Kubeclient kubernetes.Interface
	Scheme     *runtime.Scheme
}

type VsHealthCheck struct {
	IsEnable         string              `json:"hc_enabled,omitempty" default:"false"`
	IsEnableAllPorts string              `json:"enable_all_ports,omitempty" default:"true"`
	Protocol         string              `json:"protocol,omitempty"`
	HttpProtocol     string              `json:"http_protocol,omitempty"`
	Port             int                 `json:"port,omitempty"`
	HcPorts          []VsHealthCheckPort `json:"hc_ports,omitempty"`
	Interval         int                 `json:"interval,omitempty"`
	Timeout          int                 `json:"timeout,omitempty"`
	SucChecks        int                 `json:"successful_checks,omitempty"`
	FailChecks       int                 `json:"failed_checks,omitempty"`
}

type VsHealthCheckPort struct {
	Protocol string `json:"protocol,omitempty"`
	Port     int32  `json:"port,omitempty"`
}

type VsEndpointsAddress struct {
	Addresses []EndpointAdress `json:"ep_address,omitempty"`
}

type VsEndpointsNotReadyAddress struct {
	NotReadyAddresses []EndpointAdress `json:"ep_notready_address,omitempty"`
}

type EndpointAdress struct {
	EndpointNameSpace string `json:"ep_namespace"`
	EndpointIpAddress string `json:"ep_ip,omitempty"`
	EndpointNodeName  string `json:"ep_node_name,omitempty"`
	EndpointPodName   string `json:"ep_pod_name,omitempty"`
	EndpointWeight    int    `json:"ep_weight,omitempty"`
	EndpointStatus    string `json:"ep_status,omitempty"`
}

type ReqNgxDownList struct {
	Servers ServersNgxList `json:"servers,omitempty"`
}

type ServersNgxList struct {
	Total      int           `json:"total,omitempty"`
	Generation int           `json:"generation,omitempty"`
	Http       []HcNgxDetail `json:"http,omitempty"`
	Stream     []HcNgxDetail `json:"stream,omitempty"`
}

type HcNgxDetail struct {
	Index    int    `json:"index,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status,omitempty"`
	Rise     int    `json:"rise,omitempty"`
	Fall     int    `json:"fall,omitempty"`
	Type     string `json:"type,omitempty"`
	Port     int    `json:"port,omitempty"`
}

type EpCacheInfo struct {
	HcRise int `json:"rise,omitempty"`
	HcFall int `json:"fall,omitempty"`
}

//+kubebuilder:rbac:groups=lbhc.cloud.edge,resources=lbhcs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=lbhc.cloud.edge,resources=lbhcs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=lbhc.cloud.edge,resources=lbhcs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Lbhc object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.2/pkg/reconcile
func (r *LbhcReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	lbhc := &lbhcv1.Lbhc{}
	if err := r.Get(ctx, req.NamespacedName, lbhc); err == nil {

		klog.Infof("lbhc change logic: %s\n", lbhc.Name)
		/*modify the ep in NotReadyAddressList(false), AddressList(true)*/
		err = modifyEpBaseOnLbhc(lbhc)
		if err != nil {
			return ctrl.Result{}, err
		}

		err = cleanHcBaseEpUpStatus(lbhc)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	endpoints := &corev1.Endpoints{}
	if err := r.Get(ctx, req.NamespacedName, endpoints); err == nil {
		klog.Infof("endpoints change name:%s, namespace:%s\n", endpoints.Name, endpoints.Namespace)

		if endpoints.Labels[VsHcIsEnable] == "true" {
			klog.Infof("endpoints hc enable:%s\n", endpoints.Labels[VsHcIsEnable])

			err = delServerInNginxUpstream(fmt.Sprintf("%s_%s", endpoints.Name, endpoints.Namespace))
			if err != nil {
				return ctrl.Result{}, err
			}
			err = addServerInNginxUpstream(endpoints)
			if err != nil {
				return ctrl.Result{}, err
			}

			/*reload ngx's config*/
			err = reloadNgx()
			if err != nil {
				return ctrl.Result{}, err
			}
		} else if endpoints.Labels[VsHcIsEnable] == "false" {
			klog.Infof("endpoints hc disable:%s\n", endpoints.Labels[VsHcIsEnable])

			err = delServerInNginxUpstream(fmt.Sprintf("%s_%s", endpoints.Name, endpoints.Namespace))
			if err != nil {
				return ctrl.Result{}, err
			}

			/*reload ngx's config*/
			err = reloadNgx()
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	svc := &corev1.Service{}
	if err := r.Get(ctx, req.NamespacedName, svc); err == nil {
		klog.Infof("svc change name:%s, namespace:%s\n", svc.Name, svc.Namespace)

		err := syncSvcLabelToEp(svc.Name, svc.Namespace, svc)
		if err != nil {
			return ctrl.Result{}, err
		}

	}

	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err == nil {
		klog.Infof("pod change name:%s, namespace:%s\n", pod.Name, pod.Namespace)
		podLabels := pod.Labels
		for key, _ := range podLabels {
			if strings.Contains(key, VsAppLabelPrefix) {

				metaKey := strings.Split(key, "/")
				if len(metaKey) == 2 {
					metaEndpoint := strings.Split(metaKey[1], "_")
					if len(metaEndpoint) == 2 {
						epName := metaEndpoint[0]
						epNameSpace := metaEndpoint[1]
						podName := pod.Name
						podNameSpace := pod.Namespace
						podIp := pod.Status.PodIP
						nodeName := pod.Spec.NodeName
						klog.Infof("podIp:%s\n", podIp)
						err := updateEpInfo(epName, epNameSpace, podName, podNameSpace, podIp, nodeName)
						if err != nil {
							klog.Errorf("update endpoints error: %v\n", err)
							return ctrl.Result{}, err
						}
					}
				}
			}
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LbhcReconciler) SetupWithManager(mgr ctrl.Manager) error {

	LbHcClient = r.Client
	Kubeclient = r.Kubeclient
	Elect = LeaderElection()
	GlobalHostName = os.Getenv("KUBE_NODE_NAME")
	if os.Getenv("DEPLOY_MODE") == "single" {
		DeployIsCluster = false
	}

	go wait.Until(SyncHealthCheck, SyncFrequency*time.Second, wait.NeverStop)

	return ctrl.NewControllerManagedBy(mgr).
		For(&lbhcv1.Lbhc{}).
		Watches(&source.Kind{Type: &coorv1.Lease{}}, &handler.EnqueueRequestForObject{}).
		Watches(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForObject{}).
		Watches(&source.Kind{Type: &corev1.Service{}}, &handler.EnqueueRequestForObject{}).
		Watches(&source.Kind{Type: &corev1.Endpoints{}}, &handler.EnqueueRequestForObject{}).WithEventFilter(&ResourceOptChangedPredicate{}).
		Complete(r)
}

type ResourceOptChangedPredicate struct {
	predicate.Funcs
}

func (rl *ResourceOptChangedPredicate) Update(evt event.UpdateEvent) bool {

	newObj, ok1 := evt.ObjectNew.(*corev1.Endpoints)
	oldObj, ok2 := evt.ObjectOld.(*corev1.Endpoints)
	if ok1 && ok2 {
		epLabels := newObj.GetLabels()
		if _, ok := epLabels[VsDomain]; ok {
			if newObj.ResourceVersion != oldObj.ResourceVersion {
				newSvcName := fmt.Sprintf(VirtualServiceFullValue, newObj.Name, newObj.Namespace)
				if GlobalHostName == GlobalSvcLeaseDetail[newSvcName] || Elect.isLeader() {
					klog.Infof("epName: %s namespace: %s Update...", newObj.Name, newObj.Namespace)
					if !compareEpSubsets(newObj.Subsets, oldObj.Subsets) ||
						!compareMapsStrings(newObj.Labels, oldObj.Labels) {
						klog.Infof("epName: %s namespace: %s is change!", newObj.Name, newObj.Namespace)
						syncLbhcWithEndpoints(newObj.Name, newObj.Namespace)
					} else {
						klog.Infof("epName: %s namespace: %s is not change!", newObj.Name, newObj.Namespace)
						return false
					}
				}
				return true
			}
		}
	}

	newLbhcObj, ok3 := evt.ObjectNew.(*lbhcv1.Lbhc)
	oldLbhcObj, ok4 := evt.ObjectOld.(*lbhcv1.Lbhc)
	if ok3 && ok4 {
		if newLbhcObj.ResourceVersion != oldLbhcObj.ResourceVersion && newLbhcObj.GetName() == CrdName {
			klog.Info("lbhc Update\n")
			return true
		}
	}

	newSvcObj, ok5 := evt.ObjectNew.(*corev1.Service)
	oldSvcObj, ok6 := evt.ObjectOld.(*corev1.Service)
	if ok5 && ok6 {
		svcLabels := newSvcObj.GetLabels()
		if _, ok := svcLabels[VsDomain]; ok {
			cacheSvcDetail(newSvcObj)

			if newSvcObj.ResourceVersion != oldSvcObj.ResourceVersion {
				klog.Infof("svcName: %s namespace: %s Update...", newSvcObj.Name, oldSvcObj.Namespace)

				if !compareMapsStrings(newSvcObj.Labels, oldSvcObj.Labels) ||
					!compareSvcPorts(newSvcObj.Spec.Ports, oldSvcObj.Spec.Ports) {
					klog.Infof("svcName: %s namespace: %s Label or ports is change!", newSvcObj.Name, oldSvcObj.Namespace)
					return true

				} else {
					klog.Infof("svcName: %s namespace: %s Label or ports is not change!", newSvcObj.Name, oldSvcObj.Namespace)
					return false
				}
			}
		}
	}

	newPodObj, ok7 := evt.ObjectNew.(*corev1.Pod)
	oldPodObj, ok8 := evt.ObjectOld.(*corev1.Pod)
	if ok7 && ok8 {
		if newPodObj.ResourceVersion != oldPodObj.ResourceVersion {
			if /*newPodObj.OwnerReferences[0].Kind == "VirtualMachineInstance" &&*/
			newPodObj.Status.Phase == corev1.PodRunning &&
				newPodObj.Status.PodIP != "" &&
				newPodObj.DeletionTimestamp == nil &&
				newPodObj.Annotations[AnnotationFt] != "svm" {
				klog.Infof("podName: %s namespace: %s Update...", newPodObj.Name, oldPodObj.Namespace)
				var isNewPodLabelHit, isOldPodLabelHit bool

				newPodLabels := newPodObj.GetLabels()
				for l, _ := range newPodLabels {
					if strings.Contains(l, VsAppLabelPrefix) {
						isNewPodLabelHit = true
					}
				}

				oldPodLabels := oldPodObj.GetLabels()
				for l, _ := range oldPodLabels {
					if strings.Contains(l, VsAppLabelPrefix) {
						isOldPodLabelHit = true
					}
				}

				if isNewPodLabelHit || isOldPodLabelHit {
					klog.Infof("podName: %s namespace: %s Hit", newPodObj.Name, oldPodObj.Namespace)
					err := syncEndpointsWithPodLabel(newPodObj, oldPodObj)
					if err != nil {
						klog.Error(err)
					}
				}

				return true
			}
		}

	}

	newLeaseObj, ok9 := evt.ObjectNew.(*coorv1.Lease)
	oldLeaseObj, ok10 := evt.ObjectOld.(*coorv1.Lease)
	if ok9 && ok10 {
		isPrefix := strings.HasPrefix(newLeaseObj.Name, "kubevip-")
		if isPrefix {
			if newLeaseObj.ResourceVersion != oldLeaseObj.ResourceVersion &&
				*newLeaseObj.Spec.HolderIdentity != *oldLeaseObj.Spec.HolderIdentity {
				klog.Infof("Lease %s-%s Update\n", newLeaseObj.Name, newLeaseObj.Namespace)
				if newLeaseObj.Spec.HolderIdentity != nil {
					tempLeaseName := strings.Split(newLeaseObj.Name, "kubevip-")
					if len(tempLeaseName) != 2 {
						klog.Infof("Lease %s-%s Update error\n", newLeaseObj.Name, newLeaseObj.Namespace)
						return false
					}
					leaseName := fmt.Sprintf(VirtualServiceFullValue, tempLeaseName[1], newLeaseObj.Namespace)
					if GlobalSvcLeaseDetail == nil {
						GlobalSvcLeaseDetail = make(map[string]string)
					}
					GlobalSvcLeaseDetail[leaseName] = *newLeaseObj.Spec.HolderIdentity
				} else {
					tempLeaseName := strings.Split(newLeaseObj.Name, "kubevip-")
					if len(tempLeaseName) != 2 {
						klog.Infof("Lease %s-%s Update error\n", newLeaseObj.Name, newLeaseObj.Namespace)
						return false
					}
					leaseName := fmt.Sprintf(VirtualServiceFullValue, tempLeaseName[1], newLeaseObj.Namespace)
					if GlobalSvcLeaseDetail == nil {
						GlobalSvcLeaseDetail = make(map[string]string)
					}
					GlobalSvcLeaseDetail[leaseName] = ""
				}
				klog.V(5).Infof("Lease %s-%s Update success\n", newLeaseObj.Name, newLeaseObj.Namespace)
				return true
			}
		}
	}

	return false
}

func (rl *ResourceOptChangedPredicate) Create(evt event.CreateEvent) bool {

	epObj, ok1 := evt.Object.(*corev1.Endpoints)
	if ok1 {
		epLabels := epObj.GetLabels()
		if _, ok := epLabels[VsDomain]; ok {
			klog.Infof("epName: %s namespace: %s Create...", epObj.Name, epObj.Namespace)
			return true
		}
	}

	hcObj, ok2 := evt.Object.(*lbhcv1.Lbhc)
	if ok2 {
		hcLabels := hcObj.GetLabels()
		if _, ok := hcLabels[VsDomain]; ok {
			klog.Info("Lbhc Create\n")
			return true
		}
	}

	svcObj, ok3 := evt.Object.(*corev1.Service)
	if ok3 {
		svcLabels := svcObj.GetLabels()
		if _, ok := svcLabels[VsDomain]; ok {
			svcName := svcObj.Name
			svcNameSpace := svcObj.Namespace
			klog.Infof("svcName: %s namespace: %s Create...", svcName, svcNameSpace)

			cacheSvcDetail(svcObj)

			err := createEpBaseSvc(svcObj)
			klog.Error(err)

			return err == nil
		}
	}

	podObj, ok4 := evt.Object.(*corev1.Pod)
	if ok4 {
		if podObj.Status.Phase == corev1.PodRunning && podObj.DeletionTimestamp == nil {
			podLabels := podObj.GetLabels()
			for lk, _ := range podLabels {
				if strings.Contains(lk, VsAppLabelPrefix) {
					klog.Infof("podName: %s namespace: %s Create...", podObj.Name, podObj.Namespace)
					metaKey := strings.Split(lk, "/")
					if len(metaKey) == 2 {
						metaEndpoint := strings.Split(metaKey[1], "_")
						if len(metaEndpoint) == 2 {
							svcName := metaEndpoint[0]
							svcNameSpace := metaEndpoint[1]
							podName := podObj.Name
							podNameSpace := podObj.Namespace
							podIp := podObj.Status.PodIP
							nodeName := podObj.Spec.NodeName

							svc, err := Kubeclient.CoreV1().Services(svcNameSpace).Get(context.TODO(), svcName, metav1.GetOptions{})
							if err != nil {
								klog.Errorf("get svc Error: %s", err)
								return true
							}
							err = createEpInfo(svc, podName, podNameSpace, podIp, nodeName)
							if err != nil {
								klog.Errorf("update endpoints error: %v\n", err)
							}
						}
					}
				}
			}
		}
	}

	leaseObj, ok5 := evt.Object.(*coorv1.Lease)
	if ok5 {
		isPrefix := strings.HasPrefix(leaseObj.Name, "kubevip-")
		if isPrefix {
			klog.Infof("Lease %s-%s Update\n", leaseObj.Name, leaseObj.Namespace)
			if leaseObj.Spec.HolderIdentity != nil {
				tempLeaseName := strings.Split(leaseObj.Name, "kubevip-")
				if len(tempLeaseName) != 2 {
					return false
				}
				leaseName := fmt.Sprintf(VirtualServiceFullValue, tempLeaseName[1], leaseObj.Namespace)
				if GlobalSvcLeaseDetail == nil {
					GlobalSvcLeaseDetail = make(map[string]string)
				}
				GlobalSvcLeaseDetail[leaseName] = *leaseObj.Spec.HolderIdentity
			} else {
				tempLeaseName := strings.Split(leaseObj.Name, "kubevip-")
				if len(tempLeaseName) != 2 {
					return false
				}
				leaseName := fmt.Sprintf(VirtualServiceFullValue, tempLeaseName[1], leaseObj.Namespace)
				if GlobalSvcLeaseDetail == nil {
					GlobalSvcLeaseDetail = make(map[string]string)
				}
				GlobalSvcLeaseDetail[leaseName] = ""
			}
			klog.V(5).Infof("Lease %s-%s Update success\n", leaseObj.Name, leaseObj.Namespace)
			return true
		}
	}

	return false
}

func (rl *ResourceOptChangedPredicate) Generic(evt event.GenericEvent) bool {
	klog.Info("endpoints Generic\n")
	return true
}

func (rl *ResourceOptChangedPredicate) Delete(evt event.DeleteEvent) bool {

	epObj, ok1 := evt.Object.(*corev1.Endpoints)
	if ok1 {
		epLabels := epObj.GetLabels()
		if _, ok := epLabels[VsDomain]; ok {
			klog.Infof("epName: %s namespace: %s Delete...", epObj.Name, epObj.Namespace)
			svcName := fmt.Sprintf("%s_%s", epObj.Name, epObj.Namespace)

			err := delServerInNginxUpstream(svcName)
			if err == nil {
				reloadNgx()
			}

			deleteLbhcEpElemByOne(svcName)

			return true
		}
	}

	/*
		svcObj, ok2 := evt.Object.(*corev1.Service)
		if ok2 {
			svcLabels := svcObj.GetLabels()
			if _, ok := svcLabels[VsDomain]; ok {
				klog.Infof("svcName: %s namespace: %s Delete...", svcObj.Name, svcObj.Namespace)
				//delSvcWithDelPodLabel(svcObj)
				return true
			}
		}
	*/

	podObj, ok4 := evt.Object.(*corev1.Pod)
	if ok4 {
		podLabels := podObj.GetLabels()
		for lk, _ := range podLabels {
			if strings.Contains(lk, VsAppLabelPrefix) {
				klog.Infof("podName: %s namespace: %s Delete...", podObj.Name, podObj.Namespace)
				metaKey := strings.Split(lk, "/")
				if len(metaKey) == 2 {
					metaEndpoint := strings.Split(metaKey[1], "_")
					if len(metaEndpoint) == 2 {
						podIp := podObj.Status.PodIP
						epName := metaEndpoint[0]
						epNameSpace := metaEndpoint[1]
						//podName := newPodObj.Name
						//podNameSpace := newPodObj.Namespace
						//nodeName := newPodObj.Spec.NodeName
						err := deleteEp(epName, epNameSpace, podIp)
						if err != nil {
							klog.Errorf("delete endpoints error: %v\n", err)
							//return false
						}
					}
				}
			}

		}

	}

	return false
}

/*
func compareMaps(data1 map[string]bool, data2 map[string]bool) bool {
	if data1 == nil || data2 == nil {
		return false
	}
	keySlice := make([]string, 0)
	dataSlice1 := make([]interface{}, 0)
	dataSlice2 := make([]interface{}, 0)
	for key, value := range data1 {
		keySlice = append(keySlice, key)
		dataSlice1 = append(dataSlice1, value)
	}
	for _, key := range keySlice {
		if data, ok := data2[key]; ok {
			dataSlice2 = append(dataSlice2, data)
		} else {
			return false
		}
	}
	dataStr1, _ := json.Marshal(dataSlice1)
	dataStr2, _ := json.Marshal(dataSlice2)

	return string(dataStr1) == string(dataStr2)
}
*/

func compareMapsStrings(data1 map[string]string, data2 map[string]string) bool {
	if data1 == nil || data2 == nil {
		return false
	}
	keySlice := make([]string, 0)
	dataSlice1 := make([]interface{}, 0)
	dataSlice2 := make([]interface{}, 0)
	for key, value := range data1 {
		keySlice = append(keySlice, key)
		dataSlice1 = append(dataSlice1, value)
	}
	for _, key := range keySlice {
		if data, ok := data2[key]; ok {
			dataSlice2 = append(dataSlice2, data)
		} else {
			return false
		}
	}
	dataStr1, _ := json.Marshal(dataSlice1)
	dataStr2, _ := json.Marshal(dataSlice2)

	return string(dataStr1) == string(dataStr2)
}

func compareMaps(new map[string]map[string]bool, old map[string]map[string]bool) bool {
	klog.V(9).Infof("new: %v - %d, old: %v - %d\n", new, len(new), old, len(old))
	if len(new) == 0 && len(old) == 0 {
		return true
	}

	if new == nil || old == nil {
		return false
	}
	keySlice := make([]map[string]string, 0)

	dataSlice1 := make([]interface{}, 0)
	dataSlice2 := make([]interface{}, 0)
	for key, value := range new {

		for k, v := range value {
			elem := make(map[string]string, 0)
			elem[key] = k
			keySlice = append(keySlice, elem)
			dataSlice1 = append(dataSlice1, v)
		}
	}

	for _, newSlice := range keySlice {

		for key, value := range newSlice {
			if data, ok := old[key][value]; ok {
				dataSlice2 = append(dataSlice2, data)
			} else {
				return false
			}
		}
	}

	dataStr1, _ := json.Marshal(dataSlice1)
	dataStr2, _ := json.Marshal(dataSlice2)

	ret := string(dataStr1) == string(dataStr2)
	klog.Infof("compareMaps is :%v\n", ret)
	return ret
}

func compareEpSubsets(newSubsets []corev1.EndpointSubset, oldSubsets []corev1.EndpointSubset) bool {
	if len(newSubsets) != len(oldSubsets) {
		klog.Info("not same\n")
		return false
	}

	for index, newObj := range newSubsets {
		//compare Address
		newAddress := newObj.Addresses
		if oldSubsets[index].Addresses == nil {
			klog.Info("old address is nil, not same\n")
			return false
		}
		oldAddress := oldSubsets[index].Addresses
		if !compareEpAddress(newAddress, oldAddress) {
			klog.Info("address is not same\n")
			return false
		}

		//compare NotReadyAddress
		newNotReadyAddress := newObj.NotReadyAddresses
		if oldSubsets[index].NotReadyAddresses == nil {
			klog.Info("old notready address is nil, not same\n")
			return false
		}
		oldNotReadyAddress := oldSubsets[index].NotReadyAddresses
		if !compareEpAddress(newNotReadyAddress, oldNotReadyAddress) {
			klog.Info("not ready address is not same\n")
			return false
		}

		//compare ports
		newPorts := newObj.Ports
		if oldSubsets[index].Ports == nil {
			klog.Info("old ports is nil, not same\n")
			return false
		}
		oldPorts := oldSubsets[index].Ports

		if !compareEpPorts(newPorts, oldPorts) {
			klog.Info("ports is not same\n")
			return false
		}

	}

	return true
}

func compareEpAddress(newAddress []corev1.EndpointAddress, oldAddress []corev1.EndpointAddress) bool {
	if len(newAddress) != len(oldAddress) {
		return false
	}

	for _, newIp := range newAddress {
		isFind := false
		for _, oldIp := range oldAddress {
			if newIp.IP == oldIp.IP {
				isFind = true
				break
			}
		}

		if !isFind {
			return false
		}
	}

	return true
}

func compareEpPorts(newPorts []corev1.EndpointPort, oldPorts []corev1.EndpointPort) bool {
	if len(newPorts) != len(oldPorts) {
		return false
	}

	for _, new_port := range newPorts {

		isFind := false
		for _, old_port := range oldPorts {
			if new_port.Port == old_port.Port && new_port.Protocol == old_port.Protocol {
				isFind = true
				break
			}
		}

		if !isFind {
			return false
		}
	}

	return true
}

func compareSvcPorts(newPorts []corev1.ServicePort, oldPorts []corev1.ServicePort) bool {
	if len(newPorts) != len(oldPorts) {
		return false
	}

	for _, new_port := range newPorts {

		isFind := false
		for _, old_port := range oldPorts {
			if new_port.Port == old_port.Port && new_port.Protocol == old_port.Protocol &&
				new_port.TargetPort == old_port.TargetPort {
				isFind = true
				break
			}
		}

		if !isFind {
			return false
		}
	}

	return true
}

func SyncHealthCheck() {

	req := &HTTPClient{
		restURL: NgxServiceAddress + ":" + NgxServicePort,
		debug:   true,
	}
	body, err := req.Get("/status?status="+NgxServiceHcUrl, JSONContentType)
	if err != nil {
		klog.Infof("Hc Get error:%v\n", err)
		reloadNgx()
		return
	}
	req.debugLogf("Response %s, Loop:%d\n", string(body), GlobalSyncLoop)

	var payload ReqNgxDownList
	if err := json.Unmarshal(body, &payload); err != nil {
		klog.Errorf("Unmarshal body Error: %s", err)
		return
	}

	switch {
	case payload.Servers.Total > 0:
		eventOpt(payload)

	case payload.Servers.Total == 0:
		noneEventOpt()
	}
}

func eventOpt(payload ReqNgxDownList) {
	var recordEpIpDownDict map[string]bool
	var lbhc lbhcv1.Lbhc
	var compareDownEpList map[string]map[string]bool

	if GlobalHostName == "" {
		GlobalHostName = os.Getenv("KUBE_NODE_NAME")
	}

	recordEpIpDownDict = make(map[string]bool)
	GlobalLbhcLastNotDownStatus = false

	if err := LbHcClient.Get(context.TODO(), types.NamespacedName{Namespace: CrdNamespace, Name: CrdName}, &lbhc); err != nil {
		klog.Errorf("unable to fetch downEpList: %v\n", err)

		if apierrors.IsNotFound(err) {
			lbhc = lbhcv1.Lbhc{
				ObjectMeta: metav1.ObjectMeta{
					Name:      CrdName,
					Namespace: CrdNamespace,
					Labels:    map[string]string{VsDomain: VsDomainName},
				},
				Spec: lbhcv1.LbhcSpec{
					DownEpList: make(map[string]map[string]bool),
				},
			}

			if err := LbHcClient.Create(context.TODO(), &lbhc, &client.CreateOptions{}); err != nil {
				klog.Errorf("create LbHc error: %v\n", err)
			}
		}
	}

	/*if GlobalSyncLoop == 0 {
		deleteLbhcEpElemByAll()
	}
	*/

	GlobalLbhc = *lbhc.DeepCopy()

	compareDownEpList = lbhc.Spec.DeepCopy().DownEpList

	for _, down := range payload.Servers.Http {
		klog.Infof("hc http down: %s, ip:%s, rise:%d, fall:%d\n", down.Upstream, down.Name, down.Rise, down.Fall)

		if !checkSvcIsValid(down.Upstream, GlobalHostName, &lbhc) {
			recordEpIpDownDict[down.Name] = true
			continue
		}

		/*skip rise*/
		if down.Rise > 0 {
			continue
		}

		recordEpIpDownDict[down.Name] = true

		if _, ok := lbhc.Spec.DownEpList[down.Upstream]; ok {

			lbhc.Spec.DownEpList[down.Upstream][down.Name] = false

		} else {
			newEpDetail := make(map[string]bool)
			newEpDetail[down.Name] = false
			if lbhc.Spec.DownEpList == nil {
				lbhc.Spec.DownEpList = make(map[string]map[string]bool)
			}
			lbhc.Spec.DownEpList[down.Upstream] = newEpDetail
		}
	}

	for _, down := range payload.Servers.Stream {
		klog.Infof("hc stream down: %s, ip:%s, rise:%d, fall:%d\n", down.Upstream, down.Name, down.Rise, down.Fall)

		if !checkSvcIsValid(down.Upstream, GlobalHostName, &lbhc) {
			recordEpIpDownDict[down.Name] = true
			continue
		}

		/*skip rise*/
		if down.Rise > 0 {
			continue
		}

		recordEpIpDownDict[down.Name] = true

		if _, ok := lbhc.Spec.DownEpList[down.Upstream]; ok {

			lbhc.Spec.DownEpList[down.Upstream][down.Name] = false

		} else {
			newEpDetail := make(map[string]bool)
			newEpDetail[down.Name] = false
			if lbhc.Spec.DownEpList == nil {
				lbhc.Spec.DownEpList = make(map[string]map[string]bool)
			}
			lbhc.Spec.DownEpList[down.Upstream] = newEpDetail
		}
	}

	/*Traverse map: Set the ep whose previous status was down
	base on the ngx down event, the pre step set status of ep to 'false', and then
	other ep's status is still 'false', so must set there is 'true'.
	*/
	klog.Infof("recordEpIpDownDict:%v\n", recordEpIpDownDict)
	for svcName, eps := range lbhc.Spec.DownEpList {
		for epIp, downStatus := range eps {
			if !recordEpIpDownDict[epIp] /*|| (GlobalSyncLoop%50 == 0)*/ {

				if !downStatus {
					lbhc.Spec.DownEpList[svcName][epIp] = true
				}
			}
		}
	}

	klog.Infof("DownEplist:%v, compareDown:%v\n", lbhc.Spec.DownEpList, compareDownEpList)
	if !compareMaps(lbhc.Spec.DownEpList, compareDownEpList) {
		/*update Lbhc*/
		klog.Info("LbHc update...\n")
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := LbHcClient.Update(context.TODO(), &lbhc, &client.UpdateOptions{}); err != nil {
				klog.Errorf("Update LbHc error: %v\n", err)
				return err
			} else {
				GlobalLbhc = *lbhc.DeepCopy()
			}
			return nil
		})
		if retryErr != nil {
			klog.Errorf("Update LbHc error: %v\n", retryErr)
		}
	}

	GlobalSyncLoop++
}

func noneEventOpt() {
	var lbhc lbhcv1.Lbhc
	/*programe first running and Zero events, so must Get, and cache in GlobalLbhc*/
	if GlobalSyncLoop == 0 {
		klog.Info("LbHc info Total==0 and GlobalSyncLoop==0 ...\n")
		if err := LbHcClient.Get(context.TODO(), types.NamespacedName{Namespace: CrdNamespace, Name: CrdName}, &lbhc); err != nil {
			klog.Errorf("unable to fetch downEpList: %v\n", err)
			return
		} else {
			GlobalSyncLoop++
			GlobalLbhc = *lbhc.DeepCopy()
		}
	} else {
		lbhc = GlobalLbhc
		klog.V(9).Infof("GlobalLbhc:%v\n", lbhc)
	}

	isModify := false
	/* None down ep, then set status == true base on lbhc's spec status*/
	for svcName, eps := range lbhc.Spec.DownEpList {
		for epIp, downStatus := range eps {
			if !downStatus {
				isModify = true
				lbhc.Spec.DownEpList[svcName][epIp] = true
			}
		}
	}

	if isModify {
		/*update Lbhc*/
		klog.Info("LbHc update...\n")
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := LbHcClient.Update(context.TODO(), &lbhc, &client.UpdateOptions{}); err != nil {
				klog.Errorf("Update LbHc error: %v\n", err)
				return err
			} else {
				GlobalLbhc = *lbhc.DeepCopy()
			}
			return nil
		})
		if retryErr != nil {
			klog.Errorf("Update LbHc error: %v\n", retryErr)
		}
	}
}

func checkSvcIsValid(svcName, hostName string, lbhc *lbhcv1.Lbhc) bool {
	mutex.Lock()
	defer mutex.Unlock()

	if GlobalSvcIsLb[svcName] { //type of loadbalancer
		if GlobalSvcLeaseDetail[svcName] != "" && GlobalSvcLeaseDetail[svcName] == hostName {
			klog.Infof("HostName %s Lease is leader\n", GlobalHostName)
			return true
		}
	} else { //type of other
		if Elect.isLeader() {
			klog.Infof("HostName %s Elector is leader\n", GlobalHostName)
			return true
		}
	}

	if _, ok := lbhc.Spec.DownEpList[svcName]; ok {
		klog.Infof("Check and Delete :%s\n", svcName)
		delete(lbhc.Spec.DownEpList, svcName)
	}
	return false
}

/*
1. judge base downStatus of  lbhc's DownEpList; format like: 192.168.0.100:80: false.
2. first notReady address move to ready address.
3. second ready address move to notReady address.
*/
func modifyEpBaseOnLbhc(lbhc *lbhcv1.Lbhc) error {

	var errRet error = nil
	var endPointsName, compareIp []string

	/*set ready Address of endpoints, notReady address move to ready address*/
	for epName, eps := range lbhc.Spec.DownEpList {

		if endPointsName = strings.Split(epName, "_"); len(endPointsName) != 2 {
			klog.Errorf("failed the lbhc's endpoints Name %s, format like: svcname_svcnamespace", epName)
			continue
		}

		for epIp, downStatus := range eps {
			if downStatus {
				if compareIp = strings.Split(epIp, ":"); len(compareIp) == 2 {
					if epInNoReadyAddressList(endPointsName[0], endPointsName[1], compareIp[0]) {
						err := modifyEpInAddressList(endPointsName[0], endPointsName[1], compareIp[0])
						if err != nil {
							errRet = err
							klog.Errorf("endpoints %s modify error: %v", endPointsName[0], err)
						}
					}
				}
			}
		}
	}

	/*wait other programe modified the endpoints*/
	time.Sleep(WaitSetDownFrequency * time.Second)

	/*set NotReady Address of endpoints, ready address move to notReady address*/
	for epName, eps := range lbhc.Spec.DownEpList {
		if endPointsName = strings.Split(epName, "_"); len(endPointsName) != 2 {
			klog.Errorf("failed the lbhc's endpoints Name %s, format like: svcname_svcnamespace", epName)
			continue
		}

		for epIp, downStatus := range eps {
			if !downStatus {
				if compareIp = strings.Split(epIp, ":"); len(compareIp) == 2 {
					if epInReadyAddressList(endPointsName[0], endPointsName[1], compareIp[0]) {
						err := modifyEpInNotReadyAddressList(endPointsName[0], endPointsName[1], compareIp[0])
						if err != nil {
							errRet = err
							klog.Errorf("endpoints %s modify error: %v", endPointsName[0], err)
						}
					}
				}
			}
		}
	}

	return errRet
}

func syncLbhcWithEndpoints(tarEpName, tarEpNameSpace string) error {
	//mutex.Lock()
	//defer mutex.Unlock()

	if tarEpName == "" || tarEpNameSpace == "" {
		return fmt.Errorf("ep name or ep namespace is Null")
	}

	var compareIp []string
	var err error
	var lbhc lbhcv1.Lbhc

	if err = LbHcClient.Get(context.TODO(), types.NamespacedName{Namespace: CrdNamespace, Name: CrdName}, &lbhc); err == nil {
		klog.Infof("sync lbhc with endpoints name:%s, namespace:%s\n", tarEpName, tarEpNameSpace)
		/*set NotReady Address of endpoints, ready address move to notReady address*/

		curEpNameKey := fmt.Sprintf("%s_%s", tarEpName, tarEpNameSpace)
		curEpDownValue := lbhc.Spec.DownEpList[curEpNameKey]

		for epIp, downStatus := range curEpDownValue {
			if !downStatus {
				if compareIp = strings.Split(epIp, ":"); len(compareIp) == 2 {
					if epInReadyAddressList(tarEpName, tarEpNameSpace, compareIp[0]) {
						err := modifyEpInNotReadyAddressList(tarEpName, tarEpNameSpace, compareIp[0])
						if err != nil {
							klog.Errorf("endpoints %s modify error: %v", tarEpName, err)
						}
					}
				}
			}
		}

	} else {
		klog.Errorf("unable to fetch downEpList: %v\n", err)
	}

	return err
}

func cleanHcBaseEpUpStatus(lbhc *lbhcv1.Lbhc) error {
	mutex.Lock()
	defer mutex.Unlock()

	var errRet error = nil
	var isModified bool = false

	klog.Infof("Clean Hc Status ...\n")
	for _, eps := range lbhc.Spec.DownEpList {

		for epIp, status := range eps {
			if status {
				isModified = true
				klog.Infof("Delete Hc Ip:%s\n", epIp)
				delete(eps, epIp)
			}
		}
	}

	if isModified {
		klog.Info("LbHc update...\n")
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := LbHcClient.Update(context.TODO(), lbhc, &client.UpdateOptions{}); err != nil {
				klog.Errorf("Update LbHc error: %v\n", err)
				return err
			}
			return nil
		})
		if retryErr != nil {
			klog.Errorf("Update LbHc error: %v\n", retryErr)
		}
	}

	return errRet
}

/*ready address move to notReady address*/
func modifyEpInNotReadyAddressList(epName, namespace, epIp string) error {

	//mutex.Lock()
	//defer mutex.Unlock()

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(namespace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get endpoints Error: %s", err)
	}

	var isModified bool
	tmpAddress := make([]corev1.EndpointAddress, 0)
	for index, sub := range k8sEndpoints.Subsets {
		if len(sub.Addresses) != 0 {

			var tmpNotReadyAddress *corev1.EndpointAddress

			for _, addr := range sub.Addresses {
				if addr.IP == epIp {
					isModified = true
					//sub.NotReadyAddresses = append(sub.NotReadyAddresses, addr)
					//sub.Addresses = append(sub.Addresses[:index], sub.Addresses[index+1:]...)
					tmpNotReadyAddress = addr.DeepCopy()
					if sub.NotReadyAddresses == nil {
						k8sEndpoints.Subsets[index].NotReadyAddresses = make([]corev1.EndpointAddress, 0)
					}
					k8sEndpoints.Subsets[index].NotReadyAddresses =
						append(k8sEndpoints.Subsets[index].NotReadyAddresses, *tmpNotReadyAddress)
					//k8sEndpoints.Subsets[index].Addresses = append(k8sEndpoints.Subsets[index].Addresses[:i], k8sEndpoints.Subsets[index].Addresses[i+1:]...)
				} else {
					tmpAddress = append(tmpAddress, addr)
				}
			}
			k8sEndpoints.Subsets[index].Addresses = tmpAddress
		}
	}

	if isModified {
		klog.Infof("update the ep, put in NotReadyAddresses... name:%s, epIp:%s\n", k8sEndpoints.Name, epIp)

		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			_, err := Kubeclient.CoreV1().Endpoints(namespace).Update(context.TODO(), k8sEndpoints, metav1.UpdateOptions{})
			if err != nil {
				klog.Errorf("update endpoints Error: %v", err)
				return err
			}
			return nil
		})
		if retryErr != nil {
			klog.Errorf("update endpoints Error: %v", retryErr)
		}
	}

	return nil
}

/*notReady address move to ready address*/
func modifyEpInAddressList(epName, namespace, epIp string) error {
	//mutex.Lock()
	//defer mutex.Unlock()

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(namespace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get endpoints Error: %s", err)
	}

	var isModified bool
	tmpNotReadyAddress := make([]corev1.EndpointAddress, 0)
	for index, sub := range k8sEndpoints.Subsets {
		if len(sub.NotReadyAddresses) != 0 {

			var tmpAddress *corev1.EndpointAddress

			for _, addr := range sub.NotReadyAddresses {
				if addr.IP == epIp {
					isModified = true
					//sub.Addresses = append(sub.Addresses, addr)
					tmpAddress = addr.DeepCopy()
					if sub.Addresses == nil {
						k8sEndpoints.Subsets[index].Addresses = make([]corev1.EndpointAddress, 0)
					}
					k8sEndpoints.Subsets[index].Addresses =
						append(k8sEndpoints.Subsets[index].Addresses, *tmpAddress)
					//k8sEndpoints.Subsets[index].NotReadyAddresses = append(k8sEndpoints.Subsets[index].NotReadyAddresses[:i], k8sEndpoints.Subsets[index].NotReadyAddresses[i+1:]...)
				} else {
					tmpNotReadyAddress = append(tmpNotReadyAddress, addr)
				}

			}
			k8sEndpoints.Subsets[index].NotReadyAddresses = tmpNotReadyAddress
		}
	}

	if isModified {
		klog.Infof("update the ep, put in Addresses... name:%s, epIp:%s, v:%s\n", k8sEndpoints.Name, epIp, k8sEndpoints.ResourceVersion)

		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			new, err := Kubeclient.CoreV1().Endpoints(namespace).Update(context.TODO(), k8sEndpoints, metav1.UpdateOptions{})
			klog.Infof("new version:%s\n", new.ResourceVersion)
			if err != nil {
				klog.Errorf("update endpoints Error: %v", err)
				return err
			}
			return nil
		})
		if retryErr != nil {
			klog.Errorf("update endpoints Error: %v\n", retryErr)
		}
	}
	return nil
}

func epInNoReadyAddressList(epName, namespace, epIp string) bool {

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(namespace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("get endpoints Error: %s", err)
		return false
	}

	for _, sub := range k8sEndpoints.Subsets {
		if len(sub.NotReadyAddresses) != 0 {
			for _, addr := range sub.NotReadyAddresses {
				if addr.IP == epIp {
					return true
				}
			}
		}
	}

	return false
}

func epInReadyAddressList(epName, namespace, epIp string) bool {

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(namespace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("get endpoints Error: %s", err)
		return false
	}

	for _, sub := range k8sEndpoints.Subsets {
		if len(sub.Addresses) != 0 {
			for _, addr := range sub.Addresses {
				if addr.IP == epIp {
					return true
				}
			}
		}
	}

	return false
}

func deleteLbhcEpElemByOne(svcName string) error {
	mutex.Lock()
	defer mutex.Unlock()

	var lbhc lbhcv1.Lbhc
	if err := LbHcClient.Get(context.TODO(), types.NamespacedName{Namespace: CrdNamespace, Name: CrdName}, &lbhc); err != nil {
		return err
	}

	for name, _ := range lbhc.Spec.DownEpList {
		if name == svcName {
			delete(lbhc.Spec.DownEpList, name)
		}
	}

	/*update Lbhc*/
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		klog.Info("LbHc delete...\n")
		if err := LbHcClient.Update(context.TODO(), &lbhc, &client.UpdateOptions{}); err != nil {
			klog.Errorf("Update LbHc error: %v\n", err)
			return err
		}
		return nil
	})
	if retryErr != nil {
		klog.Errorf("Update LbHc error: %v\n", retryErr)
	}

	return nil
}

/*
func deleteLbhcEpElemByAll() error {
	mutex.Lock()
	defer mutex.Unlock()

	var lbhc lbhcv1.Lbhc
	if err := LbHcClient.Get(context.TODO(), types.NamespacedName{Namespace: CrdNamespace, Name: CrdName}, &lbhc); err != nil {
		return err
	}

	for name, _ := range lbhc.Spec.DownEpList {
		delete(lbhc.Spec.DownEpList, name)
	}


	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		klog.Info("LbHc delete...\n")
		if err := LbHcClient.Update(context.TODO(), &lbhc, &client.UpdateOptions{}); err != nil {
			klog.Errorf("Update LbHc error: %v\n", err)
			return err
		}
		return nil
	})
	if retryErr != nil {
		klog.Errorf("Update LbHc error: %v\n", retryErr)
	}

	return nil
}
*/

func getHcFromEndpoints(endpoints *corev1.Endpoints) (*VsHealthCheck, error) {

	var hcStruct VsHealthCheck
	var err error

	if endpoints.Labels[VsHcIsEnable] != "" {
		hcStruct.IsEnable = endpoints.Labels[VsHcIsEnable]
		if endpoints.Labels[VsHcIsEnableAllPorts] != "" {
			hcStruct.IsEnableAllPorts = endpoints.Labels[VsHcIsEnableAllPorts]
		} else {
			hcStruct.IsEnableAllPorts = "true"
		}

		if strings.EqualFold(hcStruct.IsEnableAllPorts, "true") {
			for _, sub := range endpoints.Subsets {
				if len(sub.Ports) != 0 {
					hcStruct.HcPorts = getK8sPortsToVsHcPort(endpoints)
				}
			}
		}

		if endpoints.Labels[VsHcHttpProtocol] != "" {
			hcStruct.HttpProtocol = endpoints.Labels[VsHcHttpProtocol]
		}

		if endpoints.Labels[VsHcProtocolPort] != "" {

			if strings.EqualFold(hcStruct.IsEnableAllPorts, "false") {
				var newPortsProto = strings.Split(endpoints.Labels[VsHcProtocolPort], "-")
				if len(newPortsProto) != 2 {
					klog.Infof("Labels invalid value %s", newPortsProto)
				}
				hcStruct.Port, _ = strconv.Atoi(newPortsProto[0])
				hcStruct.Protocol = newPortsProto[1]

				var ports []VsHealthCheckPort
				ports = append(ports, VsHealthCheckPort{hcStruct.Protocol, int32(hcStruct.Port)})
				hcStruct.HcPorts = ports
			} else {
				for _, sub := range endpoints.Subsets {
					if len(sub.Ports) != 0 {
						hcStruct.HcPorts = getK8sPortsToVsHcPort(endpoints)
					}
				}
			}
		} else {
			if hcStruct.HttpProtocol != "" {
				hcStruct.Protocol = hcStruct.HttpProtocol
			} else {
				hcStruct.Protocol = "tcp"
			}
		}

		if endpoints.Labels[VsHcMonitorParam] != "" {
			var newParameter = strings.Split(endpoints.Labels[VsHcMonitorParam], "-")
			if len(newParameter) != 4 {
				klog.Infof("Labels invalid value %s", newParameter)
			}
			hcStruct.Interval, err = strconv.Atoi(newParameter[0])
			if err != nil {
				hcStruct.Interval = 3000
			}
			hcStruct.Timeout, err = strconv.Atoi(newParameter[1])
			if err != nil {
				hcStruct.Timeout = 1000
			}
			hcStruct.SucChecks, err = strconv.Atoi(newParameter[2])
			if err != nil {
				hcStruct.SucChecks = 3
			}
			hcStruct.FailChecks, err = strconv.Atoi(newParameter[3])
			if err != nil {
				hcStruct.FailChecks = 5
			}

		} else {
			hcStruct.Interval = 3000
			hcStruct.Timeout = 1000
			hcStruct.SucChecks = 3
			hcStruct.FailChecks = 5
		}
	}

	return &hcStruct, nil
}

func getK8sPortsToVsHcPort(endpoints *corev1.Endpoints) []VsHealthCheckPort {
	var ports []VsHealthCheckPort
	var httpProtocol string

	for _, sub := range endpoints.Subsets {
		for _, port := range sub.Ports {
			switch port.Protocol {
			case corev1.ProtocolTCP:
				if endpoints.Labels[VsHcHttpProtocol] != "" {
					httpProtocol = endpoints.Labels[VsHcHttpProtocol]
				} else {
					httpProtocol = string(corev1.ProtocolTCP)
				}
				ports = append(ports, VsHealthCheckPort{httpProtocol, port.Port})
			case corev1.ProtocolUDP:
				ports = append(ports, VsHealthCheckPort{string(corev1.ProtocolUDP), port.Port})
			case corev1.ProtocolSCTP:
				ports = append(ports, VsHealthCheckPort{string(corev1.ProtocolSCTP), port.Port})
			}

		}
	}
	return ports
}

func getEndpointsAddress(k8sEp *corev1.Endpoints) (*VsEndpointsAddress, error) {
	var epAddress VsEndpointsAddress
	epAddress.Addresses = make([]EndpointAdress, 0)
	var epAddr []EndpointAdress

	for _, sub := range k8sEp.Subsets {
		if len(sub.Addresses) != 0 {
			var ep EndpointAdress
			for _, addr := range sub.Addresses {
				if addr.IP != "" {
					ep.EndpointIpAddress = addr.IP
				}
				if addr.NodeName != nil {
					ep.EndpointNodeName = *addr.NodeName
				}

				if addr.TargetRef != nil {
					ep.EndpointPodName = addr.TargetRef.Name
					ep.EndpointNameSpace = addr.TargetRef.Namespace
				}

				ep.EndpointWeight = 1
				epAddr = append(epAddr, ep)
			}
		}

		if len(sub.NotReadyAddresses) != 0 {
			var ep EndpointAdress
			for _, addr := range sub.NotReadyAddresses {
				if addr.IP != "" {
					ep.EndpointIpAddress = addr.IP
				}
				if addr.NodeName != nil {
					ep.EndpointNodeName = *addr.NodeName
				}

				if addr.TargetRef != nil {
					ep.EndpointPodName = addr.TargetRef.Name
					ep.EndpointNameSpace = addr.TargetRef.Namespace
				}
				ep.EndpointWeight = 1
				epAddr = append(epAddr, ep)
			}
		}
	}

	if len(epAddr) == 0 {
		return nil, fmt.Errorf("endpoints address is nil")
	}

	epAddress.Addresses = epAddr

	return &epAddress, nil
}

func getEndpointsPort(k8sEp *corev1.Endpoints) ([]corev1.EndpointPort, error) {
	var epPorts []corev1.EndpointPort

	for _, sub := range k8sEp.Subsets {
		if len(sub.Ports) != 0 {
			epPorts = append(epPorts, sub.Ports...)
		}
	}

	if len(epPorts) == 0 {
		return nil, fmt.Errorf("endpoints ports is nil")
	}

	return epPorts, nil
}

func addServerInNginxUpstream(endpoints *corev1.Endpoints) error {

	svcNsName := fmt.Sprintf("%s_%s", endpoints.Name, endpoints.Namespace)

	upstreamBlockParam := []crossplane.Directive{}
	serverBlockParam := []crossplane.Directive{}
	tcpServerBlockParam := []crossplane.Directive{}
	udpUpstreamBlockParam := []crossplane.Directive{}
	udpServerBlockParam := []crossplane.Directive{}

	upstreamDirective := crossplane.Directive{
		Directive: "upstream",
		Args:      make([]string, 0),
		Block:     &[]crossplane.Directive{},
	}
	upstreamDirective.Args = append(upstreamDirective.Args, svcNsName)

	checkBlockDirective := crossplane.Directive{
		Directive: "check",
		Args:      make([]string, 0),
		//Block:     &[]crossplane.Directive{},
	}

	checkParameterBlockDirective := crossplane.Directive{
		Directive: "check_http_expect_alive",
		Args:      make([]string, 0),
		//Block:     &[]crossplane.Directive{},
	}
	checkParameterArray := make([]string, 0)
	checkParameterArray = append(checkParameterArray, "http_2xx")
	checkParameterArray = append(checkParameterArray, "http_3xx")
	checkParameterBlockDirective.Args = checkParameterArray

	/*check fill up*/
	hcPara, _ := getHcFromEndpoints(endpoints)

	if GlobalEpCacheInfo == nil {
		GlobalEpCacheInfo = make(map[string]EpCacheInfo)
		var info EpCacheInfo
		info.HcRise = hcPara.SucChecks
		info.HcFall = hcPara.FailChecks
		GlobalEpCacheInfo[svcNsName] = info
	} else {
		var info EpCacheInfo
		info.HcRise = hcPara.SucChecks
		info.HcFall = hcPara.FailChecks
		GlobalEpCacheInfo[svcNsName] = info
	}

	if strings.EqualFold(hcPara.IsEnableAllPorts, "false") {
		/*address fill up*/
		epAddress, err := getEndpointsAddress(endpoints)
		if err == nil {
			for _, ip := range epAddress.Addresses {
				serverBlockDirective := crossplane.Directive{
					Directive: "server",
					Args:      make([]string, 0),
					//Block:     &[]crossplane.Directive{},
				}
				serverBlockDirective.Args = append(serverBlockDirective.Args,
					fmt.Sprintf("%s:%d", ip.EndpointIpAddress, hcPara.Port))
				serverBlockParam = append(serverBlockParam, serverBlockDirective)
			}
		} else {
			return err
		}

		/*check fill up*/
		checkArray := make([]string, 0)

		checkArray = append(checkArray, fmt.Sprintf("interval=%d", hcPara.Interval))
		checkArray = append(checkArray, fmt.Sprintf("rise=%d", hcPara.SucChecks))
		checkArray = append(checkArray, fmt.Sprintf("fall=%d", hcPara.FailChecks))
		checkArray = append(checkArray, fmt.Sprintf("timeout=%d", hcPara.Timeout))
		checkArray = append(checkArray, fmt.Sprintf("type=%s", hcPara.Protocol))
		checkBlockDirective.Args = append(checkBlockDirective.Args, checkArray...)

		serverBlockParam = append(serverBlockParam, checkBlockDirective)

		if strings.EqualFold("http", hcPara.Protocol) {
			serverBlockParam = append(serverBlockParam, checkParameterBlockDirective)
		}

		upstreamDirective.Block = &serverBlockParam

		upstreamBlockParam = append(upstreamBlockParam, upstreamDirective)

		var upstreamFile string
		protocolHc := strings.ToUpper(hcPara.Protocol)
		switch protocolHc {
		case "HTTP", "TCP":
			upstreamFile = NgHttpUpstreamFile
		case "UDP":
			upstreamFile = NgSteamUpstreamFile
		default:
			upstreamFile = NgHttpUpstreamFile
		}

		return writeNginxUpstream(upstreamFile, upstreamBlockParam)
	} else {

		epPorts, err := getEndpointsPort(endpoints)
		if err == nil {
			for _, ports := range epPorts {
				/*address fill up*/
				epAddress, err := getEndpointsAddress(endpoints)
				if err == nil {

					protocolHc := strings.ToUpper(string(ports.Protocol))
					switch protocolHc {
					case "HTTP", "TCP":
						for _, ip := range epAddress.Addresses {

							serverBlockDirective := crossplane.Directive{
								Directive: "server",
								Args:      make([]string, 0),
								//Block:     &[]crossplane.Directive{},
							}
							serverBlockDirective.Args = append(serverBlockDirective.Args,
								fmt.Sprintf("%s:%d", ip.EndpointIpAddress, ports.Port))
							tcpServerBlockParam = append(tcpServerBlockParam, serverBlockDirective)

						}

						/*check fill up*/
						checkArray := make([]string, 0)

						checkArray = append(checkArray, fmt.Sprintf("interval=%d", hcPara.Interval))
						checkArray = append(checkArray, fmt.Sprintf("rise=%d", hcPara.SucChecks))
						checkArray = append(checkArray, fmt.Sprintf("fall=%d", hcPara.FailChecks))
						checkArray = append(checkArray, fmt.Sprintf("timeout=%d", hcPara.Timeout))
						if hcPara.HttpProtocol != "" {
							checkArray = append(checkArray, fmt.Sprintf("type=%s", strings.ToLower(hcPara.HttpProtocol)))
						} else {
							checkArray = append(checkArray, fmt.Sprintf("type=%s", strings.ToLower(string(ports.Protocol))))
						}
						checkBlockDirective.Args = checkArray

						/*write in file*/
						tcpServerBlockParam = append(tcpServerBlockParam, checkBlockDirective)

						if strings.EqualFold("http", hcPara.HttpProtocol) {
							tcpServerBlockParam = append(tcpServerBlockParam, checkParameterBlockDirective)
						}

					case "UDP":
						for _, ip := range epAddress.Addresses {

							serverBlockDirective := crossplane.Directive{
								Directive: "server",
								Args:      make([]string, 0),
								//Block:     &[]crossplane.Directive{},
							}
							serverBlockDirective.Args = append(serverBlockDirective.Args,
								fmt.Sprintf("%s:%d", ip.EndpointIpAddress, ports.Port))
							udpServerBlockParam = append(udpServerBlockParam, serverBlockDirective)

						}

						/*check fill up*/
						checkArray := make([]string, 0)

						checkArray = append(checkArray, fmt.Sprintf("interval=%d", hcPara.Interval))
						checkArray = append(checkArray, fmt.Sprintf("rise=%d", hcPara.SucChecks))
						checkArray = append(checkArray, fmt.Sprintf("fall=%d", hcPara.FailChecks))
						checkArray = append(checkArray, fmt.Sprintf("timeout=%d", hcPara.Timeout))
						checkArray = append(checkArray, "default_down=true")
						checkArray = append(checkArray, fmt.Sprintf("type=%s", strings.ToLower(string(ports.Protocol))))
						//checkBlockDirective.Args = append(checkBlockDirective.Args, checkArray...)
						checkBlockDirective.Args = checkArray

						/*write in file*/
						udpServerBlockParam = append(udpServerBlockParam, checkBlockDirective)

					}

				}
			}

			if len(tcpServerBlockParam) > 0 {

				upstreamDirective.Block = &tcpServerBlockParam
				upstreamBlockParam = append(upstreamBlockParam, upstreamDirective)
				writeNginxUpstream(NgHttpUpstreamFile, upstreamBlockParam)
			}
			if len(udpServerBlockParam) > 0 {

				upstreamDirective.Block = &udpServerBlockParam
				udpUpstreamBlockParam = append(udpUpstreamBlockParam, upstreamDirective)
				writeNginxUpstream(NgSteamUpstreamFile, udpUpstreamBlockParam)
			}
		} else {
			return err
		}
	}

	return nil
}

func writeNginxUpstream(upstreamFilePath string, upstreamBlockParam []crossplane.Directive) error {
	payload, err := crossplane.Parse(NginxConfigFile, &crossplane.ParseOptions{})
	if err != nil {
		klog.Errorf("Parse Error: ", err)
		return err
	}

	for _, f := range payload.Config {
		if f.File == upstreamFilePath {
			f.Parsed = append(f.Parsed, upstreamBlockParam...)

			writeCrossplane(f)
		}
	}

	return nil
}

func writeCrossplane(f crossplane.Config) error {
	var err error
	var buf bytes.Buffer
	defer buf.Reset()

	if err = crossplane.Build(&buf, f, &crossplane.BuildOptions{}); err != nil {
		klog.Errorf("Build Error: ", err)
		return err
	}
	//fmt.Printf("result: \n")
	//fmt.Println(buf.String())
	build2File := f.File
	build2Config := buf.Bytes()
	if err := os.WriteFile(build2File, build2Config, os.ModePerm); err != nil {
		klog.Errorf("WriteFile Error: ", err)
		return err
	}

	return err
}

func delServerInNginxUpstream(name string) error {

	svcNsName := name

	payload, err := crossplane.Parse(NginxConfigFile, &crossplane.ParseOptions{})
	if err != nil {
		klog.Errorf("Parse Error: ", err)
	}

	for _, f := range payload.Config {
		tempParsed := []crossplane.Directive{}
		if f.File == NgHttpUpstreamFile || f.File == NgSteamUpstreamFile {
			if len(f.Parsed) > 0 {
				for _, p := range f.Parsed {
					if p.Directive == "upstream" {
						if p.Args[0] != svcNsName {
							tempParsed = append(tempParsed, p)
						}
					}
				}

				f.Parsed = tempParsed

				writeCrossplane(f)
			}
		}
	}

	return nil
}

func reloadNgx() error {
	cmd := exec.Command(NgxBin, "-s", "reload")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return restartNgx()
	}

	return nil
}

func restartNgx() error {
	cmd := exec.Command(NgxBin, "-s", "stop")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return startNgx()
	}

	return nil
}

func startNgx() error {
	cmd := exec.Command(NgxBin)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {

		return fmt.Errorf("execute go list command, %s, stdout:%s, stderr:%s",
			err, stdout.String(), stderr.String())
	}

	return nil
}

func syncEndpointsWithPodLabel(newPodObj, oldPodObj *corev1.Pod) error {

	newLabelKey := make([]string, 0)
	//newLabelValue := make([]interface{}, 0)
	newPodLabels := newPodObj.GetLabels()
	for lk, _ := range newPodLabels {
		if strings.Contains(lk, VsAppLabelPrefix) {
			newLabelKey = append(newLabelKey, lk)
		}
	}

	oldLabelKey := make([]string, 0)
	oldPodLabels := oldPodObj.GetLabels()
	for l, _ := range oldPodLabels {
		if strings.Contains(l, VsAppLabelPrefix) {
			oldLabelKey = append(oldLabelKey, l)
		}
	}

	labelIsSame := true
	for _, key := range newLabelKey {
		if _, ok := oldPodLabels[key]; !ok { //add
			labelIsSame = false
			klog.Infof("podName:%s is add\n", newPodObj.Name)
			return nil
		}
	}
	if labelIsSame && (len(oldLabelKey) == len(newLabelKey)) {
		klog.Infof("podName:%s is mod\n", newPodObj.Name)
		return nil
	}

	for _, key := range oldLabelKey {
		if _, ok := newPodLabels[key]; !ok { //del
			klog.Infof("podName:%s is del\n", newPodObj.Name)
			metaKey := strings.Split(key, "/")
			if len(metaKey) == 2 {
				metaEndpoint := strings.Split(metaKey[1], "_")
				if len(metaEndpoint) == 2 {
					podIp := newPodObj.Status.PodIP
					epName := metaEndpoint[0]
					epNameSpace := metaEndpoint[1]
					klog.Infof("podIp:%s\n", podIp)
					err := deleteEp(epName, epNameSpace, podIp)
					if err != nil {
						klog.Errorf("delete endpoints error: %v\n", err)
						return err
					}
				}
			}
		}
	}

	return nil
}

func createEpInfo(svc *corev1.Service, podName, podNameSpace, podIp, nodeName string) error {

	err := _createEp(svc, podName, podNameSpace, podIp, nodeName)
	if err != nil {
		return err
	}

	return nil
}

func updateEpInfo(epName, epNameSpace string, podName, podNameSpace, podIp, nodeName string) error {

	namespace := epNameSpace
	svcName := epName

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(epNameSpace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("get endpoints Error: %s", err)
		return err
	}

	if len(k8sEndpoints.Subsets) == 0 {
		svc, err := Kubeclient.CoreV1().Services(namespace).Get(context.TODO(), svcName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("get svc Error: %s", err)
			return err
		}
		err = _createEp(svc, podName, podNameSpace, podIp, nodeName)
		if err != nil {
			return err
		}
	} else {
		err := _updateEp(epName, epNameSpace, podName, podNameSpace, podIp, nodeName)
		if err != nil {
			return err
		}
	}

	return nil
}

func deleteEp(epName, epNameSpace, podIp string) error {
	//mutex.Lock()
	//defer mutex.Unlock()

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(epNameSpace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("get endpoints Error: %s", err)
		return err
	}

	var isFind bool = false
	tmpNotReadyAddress := make([]corev1.EndpointAddress, 0)
	tmpAddress := make([]corev1.EndpointAddress, 0)
	for index, sub := range k8sEndpoints.Subsets {
		for _, addr := range sub.Addresses {
			if addr.IP == podIp {
				isFind = true
				//k8sEndpoints.Subsets[index].Addresses =
				//	append(k8sEndpoints.Subsets[index].Addresses[:i], k8sEndpoints.Subsets[index].Addresses[i+1:]...)
				//break
			} else {
				tmpAddress = append(tmpAddress, addr)
			}
		}
		k8sEndpoints.Subsets[index].Addresses = tmpAddress

		if !isFind {
			for _, addr := range sub.NotReadyAddresses {
				if addr.IP == podIp {
					isFind = true
					//k8sEndpoints.Subsets[index].NotReadyAddresses =
					//	append(k8sEndpoints.Subsets[index].NotReadyAddresses[:i], k8sEndpoints.Subsets[index].NotReadyAddresses[i+1:]...)
					//break
				} else {
					tmpNotReadyAddress = append(tmpNotReadyAddress, addr)
				}
			}
			k8sEndpoints.Subsets[index].NotReadyAddresses = tmpNotReadyAddress
		}
	}

	for index, sub := range k8sEndpoints.Subsets {
		if len(sub.NotReadyAddresses) == 0 && len(sub.Addresses) == 0 {
			k8sEndpoints.Subsets = append(k8sEndpoints.Subsets[:index], k8sEndpoints.Subsets[index+1:]...)
		}
	}

	if isFind {
		_, err := Kubeclient.CoreV1().Endpoints(epNameSpace).Update(context.TODO(), k8sEndpoints, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("update endpoints Error: %s", err)
			return err
		}
	}

	return nil
}

func _createEp(svc *corev1.Service, podName, podNameSpace, podIp, nodeName string) error {

	var epSubset corev1.EndpointSubset

	epName := svc.Name
	namespace := svc.Namespace

	isCreate := false

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(namespace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			isCreate = true
			klog.Infof("epName:%s epNameSpace:%s is Not Found\n", epName, namespace)
			k8sEndpoints = &corev1.Endpoints{
				ObjectMeta: metav1.ObjectMeta{
					Name:      epName,
					Namespace: namespace,
					Labels:    svc.Labels,
				},
				Subsets: make([]corev1.EndpointSubset, 0),
			}
			epSubset.Ports = getSvcPortsToEpPort(svc.Spec.Ports)
			epSubset.Addresses = make([]corev1.EndpointAddress, 0)
			k8sEndpoints.Subsets = append(k8sEndpoints.Subsets, epSubset)
		} else {
			klog.Errorf("get endpoints Error: %s", err)
			return err
		}
	}

	if podIp != "" {
		isCreate = true

		epAllAddress, err := getEndpointsALLAddress(k8sEndpoints)
		if err == nil {
			for _, epAddress := range epAllAddress.Addresses {
				if epAddress.EndpointIpAddress == podIp {
					return fmt.Errorf("endpoints Address already exists")
				}
			}
		}

		klog.Infof("epName:%s epNameSpace:%s podIp:%s\n", epName, namespace, podIp)
		var epAddr corev1.EndpointAddress
		epAddr.IP = podIp
		if nodeName != "" {
			epAddr.NodeName = &nodeName
		}

		if podName != "" {
			if epAddr.TargetRef == nil {
				epAddr.TargetRef = new(corev1.ObjectReference)
			}
			epAddr.TargetRef.Name = podName
			epAddr.TargetRef.Kind = "Pod"
			epAddr.TargetRef.Namespace = namespace
		}

		if k8sEndpoints.Subsets == nil {
			k8sEndpoints.Subsets = make([]corev1.EndpointSubset, 0)
			epSubset.Addresses = append(epSubset.Addresses, epAddr)
			k8sEndpoints.Subsets = append(k8sEndpoints.Subsets, epSubset)
		} else {
			k8sEndpoints.Subsets[0].Addresses = append(k8sEndpoints.Subsets[0].Addresses, epAddr)
			//k8sEndpoints.Subsets = append(k8sEndpoints.Subsets, epSubset)
		}

		if len(k8sEndpoints.Subsets[0].Ports) == 0 {
			k8sEndpoints.Subsets[0].Ports = getSvcPortsToEpPort(svc.Spec.Ports)
		}
	}

	if isCreate {
		if len(k8sEndpoints.Subsets) != 0 && len(k8sEndpoints.Subsets[0].Ports) == 0 {
			k8sEndpoints.Subsets[0].Ports = getSvcPortsToEpPort(svc.Spec.Ports)
		}

		klog.Infof("epName:%s, epNameSpace:%s Create...\n", epName, namespace)
		_, err = Kubeclient.CoreV1().Endpoints(namespace).Update(context.TODO(), k8sEndpoints, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("update endpoints Error: %s", err)
			return err
		}
	}

	return nil
}

func _updateEp(epName, epNameSpace string, podName, podNameSpace, podIp, nodeName string) error {

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(epNameSpace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("get endpoints Error: %s", err)
		return err
	}

	var isFind bool = false
	for _, sub := range k8sEndpoints.Subsets {
		isFind = false
		if len(sub.Addresses) != 0 {
			for _, addr := range sub.Addresses {
				if addr.IP == podIp {
					klog.Infof("epName:%s epNameSpace:%s podIp:%s\n", epName, epNameSpace, podIp)
					if addr.NodeName != &nodeName {
						isFind = true
						addr.NodeName = &nodeName
					}
					if addr.TargetRef != nil {
						if addr.TargetRef.Name != podName {
							isFind = true
							addr.TargetRef.Name = podName
						}
						if addr.TargetRef.Namespace != epNameSpace {
							isFind = true
							addr.TargetRef.Namespace = epNameSpace
						}
					} else {
						addr.TargetRef = new(corev1.ObjectReference)
						if addr.TargetRef.Name != podName {
							isFind = true
							addr.TargetRef.Name = podName
						}
						if addr.TargetRef.Namespace != epNameSpace {
							isFind = true
							addr.TargetRef.Namespace = epNameSpace
						}
					}

					break
				}
			}
		}

		if !isFind {
			if len(sub.NotReadyAddresses) != 0 {
				for _, addr := range sub.NotReadyAddresses {
					if addr.IP == podIp {
						klog.Infof("epName:%s epNameSpace:%s podIp:%s\n", epName, epNameSpace, podIp)
						if addr.NodeName != &nodeName {
							isFind = true
							addr.NodeName = &nodeName
						}
						if addr.TargetRef != nil {
							if addr.TargetRef.Name != podName {
								isFind = true
								addr.TargetRef.Name = podName
							}
							if addr.TargetRef.Namespace != epNameSpace {
								isFind = true
								addr.TargetRef.Namespace = epNameSpace
							}
						} else {
							addr.TargetRef = new(corev1.ObjectReference)
							if addr.TargetRef.Name != podName {
								isFind = true
								addr.TargetRef.Name = podName
							}
							if addr.TargetRef.Namespace != epNameSpace {
								isFind = true
								addr.TargetRef.Namespace = epNameSpace
							}
						}

						break
					}
				}
			}
		}

		if isFind {
			break
		}
	}

	if isFind { //update
		klog.Infof("update epName:%s, epNameSpace:%s Create...\n", epName, epNameSpace)
		_, err := Kubeclient.CoreV1().Endpoints(epNameSpace).Update(context.TODO(), k8sEndpoints, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("update endpoints Error: %s", err)
			return err
		}
	} else {
		namespace := epNameSpace
		svcName := epName
		svc, err := Kubeclient.CoreV1().Services(namespace).Get(context.TODO(), svcName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("get svc Error: %s", err)
			return err
		}
		klog.Infof("epName:%s, epNameSpace:%s Create...\n", epName, epNameSpace)
		err = _createEp(svc, podName, podNameSpace, podIp, nodeName)
		if err != nil {
			return err
		}
	}

	return nil
}

func syncSvcLabelToEp(epName, epNameSpace string, svc *corev1.Service) error {

	namespace := epNameSpace
	newSvcLabels := svc.GetLabels()

	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(epNameSpace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {

			klog.Infof("epName:%s epNameSpace:%s is Not Found\n", epName, namespace)
			k8sEndpoints = &corev1.Endpoints{
				ObjectMeta: metav1.ObjectMeta{
					Name:      epName,
					Namespace: namespace,
					Labels:    svc.Labels,
				},
				Subsets: make([]corev1.EndpointSubset, 0),
			}
			//epSubset.Ports = getSvcPortsToEpPort(svc.Spec.Ports)
			//k8sEndpoints.Subsets = append(k8sEndpoints.Subsets, epSubset)
		} else {
			klog.Errorf("get endpoints Error: %s", err)
			return err
		}

	}

	k8sEndpoints.Labels = newSvcLabels

	//if len(k8sEndpoints.Subsets) != 0 && len(k8sEndpoints.Subsets[0].Ports) == 0 {
	if len(k8sEndpoints.Subsets) != 0 {
		k8sEndpoints.Subsets[0].Ports = getSvcPortsToEpPort(svc.Spec.Ports)
	}

	klog.Infof("epName:%s, epNameSpace:%s Create...\n", epName, namespace)
	_, err = Kubeclient.CoreV1().Endpoints(namespace).Update(context.TODO(), k8sEndpoints, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("update endpoints Error: %s", err)
		return err
	}

	return err
}

func createEpBaseSvc(svc *corev1.Service) error {
	svcName := svc.Name
	svcNameSpace := svc.Namespace
	namespace := svcNameSpace

	full_vs_name := fmt.Sprintf(VirtualServiceFullName, svcName, svcNameSpace)
	full_vs_value := fmt.Sprintf(VirtualServiceFullValue, svcName, svcNameSpace)

	options := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", full_vs_name, full_vs_value),
	}
	podResult, err := Kubeclient.CoreV1().Pods(namespace).List(context.TODO(), options)
	if err != nil {
		return err
	}

	for _, pod := range podResult.Items {
		klog.Infof("podName:%s, status:%s\n", pod.Name, pod.Status.Phase)
		if len(pod.ObjectMeta.Labels) == 0 || pod.Status.Phase != corev1.PodRunning {
			createEpBaseSvcWithoutAddress(svc)
			continue
		} else {
			if pod.ObjectMeta.Labels != nil {
				if _, ok := pod.ObjectMeta.Labels[full_vs_name]; ok {

					podName := pod.Name
					podNameSpace := pod.Namespace
					podIp := pod.Status.PodIP
					nodeName := pod.Spec.NodeName
					err := createEpInfo(svc, podName, podNameSpace, podIp, nodeName)
					if err != nil {
						klog.Errorf("update endpoints error: %v\n", err)
						return err
					}

				}
			}
		}
	}

	return nil
}

func createEpBaseSvcWithoutAddress(svc *corev1.Service) error {
	svcName := svc.Name
	namespace := svc.Namespace
	epName := svcName
	isCreate := false
	//var epSubset corev1.EndpointSubset
	k8sEndpoints, err := Kubeclient.CoreV1().Endpoints(namespace).Get(context.TODO(), epName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			isCreate = true
			klog.Infof("epName:%s epNameSpace:%s is Not Found\n", epName, namespace)
			k8sEndpoints = &corev1.Endpoints{
				ObjectMeta: metav1.ObjectMeta{
					Name:      epName,
					Namespace: namespace,
					Labels:    svc.Labels,
				},
				Subsets: make([]corev1.EndpointSubset, 0),
			}
			//epSubset.Ports = getSvcPortsToEpPort(svc.Spec.Ports)
			//k8sEndpoints.Subsets = append(k8sEndpoints.Subsets, epSubset)
		} else {
			klog.Errorf("get endpoints Error: %s", err)
			return err
		}
	}
	if isCreate {
		if len(k8sEndpoints.Subsets) != 0 && len(k8sEndpoints.Subsets[0].Ports) == 0 {
			k8sEndpoints.Subsets[0].Ports = getSvcPortsToEpPort(svc.Spec.Ports)
		}

		klog.Infof("epName:%s, epNameSpace:%s Create...\n", epName, namespace)
		_, err = Kubeclient.CoreV1().Endpoints(namespace).Update(context.TODO(), k8sEndpoints, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("update endpoints Error: %s", err)
			return err
		}
	}
	return nil
}

func delSvcWithDelPodLabel(svc *corev1.Service) error {
	svcName := svc.Name
	svcNameSpace := svc.Namespace
	namespace := svcNameSpace

	full_vs_name := fmt.Sprintf(VirtualServiceFullName, svcName, svcNameSpace)
	full_vs_value := fmt.Sprintf(VirtualServiceFullValue, svcName, svcNameSpace)

	options := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", full_vs_name, full_vs_value),
	}
	podResult, err := Kubeclient.CoreV1().Pods(namespace).List(context.TODO(), options)
	if err != nil {
		return err
	}

	for _, pod := range podResult.Items {
		if len(pod.ObjectMeta.Labels) == 0 {
			continue
		} else {
			if pod.ObjectMeta.Labels != nil {
				if _, ok := pod.ObjectMeta.Labels[full_vs_name]; ok {
					delete(pod.ObjectMeta.Labels, full_vs_name)

					_, err := Kubeclient.CoreV1().Pods(namespace).Update(context.TODO(), &pod, metav1.UpdateOptions{})
					if err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

func getSvcPortsToEpPort(apiPorts []corev1.ServicePort) []corev1.EndpointPort {
	var ports []corev1.EndpointPort
	for _, port := range apiPorts {
		ports = append(ports, corev1.EndpointPort{Name: port.Name, Port: int32(port.TargetPort.IntValue()), Protocol: port.Protocol, AppProtocol: port.AppProtocol})
	}

	return ports
}

func getEndpointsALLAddress(k8sEp *corev1.Endpoints) (*VsEndpointsAddress, error) {
	var epAddress VsEndpointsAddress
	epAddress.Addresses = make([]EndpointAdress, 0)
	var epAddr []EndpointAdress

	for _, sub := range k8sEp.Subsets {
		if len(sub.Addresses) != 0 {
			var ep EndpointAdress
			for _, addr := range sub.Addresses {
				if addr.IP != "" {
					ep.EndpointIpAddress = addr.IP
				}
				if addr.NodeName != nil {
					ep.EndpointNodeName = *addr.NodeName
				}

				if addr.TargetRef != nil {
					ep.EndpointPodName = addr.TargetRef.Name
					ep.EndpointNameSpace = addr.TargetRef.Namespace
				}

				ep.EndpointWeight = 1
				epAddr = append(epAddr, ep)
			}
		}

		if len(sub.NotReadyAddresses) != 0 {
			var ep EndpointAdress
			for _, addr := range sub.NotReadyAddresses {
				if addr.IP != "" {
					ep.EndpointIpAddress = addr.IP
				}
				if addr.NodeName != nil {
					ep.EndpointNodeName = *addr.NodeName
				}

				if addr.TargetRef != nil {
					ep.EndpointPodName = addr.TargetRef.Name
					ep.EndpointNameSpace = addr.TargetRef.Namespace
				}
				ep.EndpointWeight = 1
				epAddr = append(epAddr, ep)
			}
		}
	}

	//if len(epAddr) == 0 {
	//	return nil, fmt.Errorf("endpoints address is nil")
	//}

	epAddress.Addresses = epAddr

	return &epAddress, nil
}

func findLeaderBaseLbip(lbip string) string {

	var leaderSvcName, leaderSvcNameSpace string

	options := metav1.ListOptions{
		//LabelSelector: fmt.Sprintf("%s=%s", VsDomain, VsDomainName),
		LabelSelector: VsKubeVipIpamAddress,
	}
	vsList, err := Kubeclient.CoreV1().Services(corev1.NamespaceAll).List(context.TODO(), options)
	if err != nil {
		return ""
	}

	for _, vs := range vsList.Items {

		if vs.Spec.Type == corev1.ServiceTypeLoadBalancer && vs.ObjectMeta.Labels[VsKubeVipIpamAddress] != "" {
			if vs.Spec.LoadBalancerIP == lbip &&
				(vs.Annotations[VsVipIgnore] == "false" || vs.Annotations[VsVipIgnore] == "") {
				leaderSvcName = vs.Name
				leaderSvcNameSpace = vs.Namespace
				break
			}
		}
	}

	if leaderSvcName != "" {
		lease, err := Kubeclient.CoordinationV1().Leases(leaderSvcNameSpace).Get(context.TODO(), "kubevip-"+leaderSvcName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Get Leases Error: %v", err)
			return ""
		}
		if *lease.Spec.HolderIdentity != "" {
			return *lease.Spec.HolderIdentity
		} else {
			return ""
		}
	}

	return ""
}

func cacheSvcDetail(svcObj *corev1.Service) {
	newSvcName := fmt.Sprintf(VirtualServiceFullValue, svcObj.Name, svcObj.Namespace)
	if DeployIsCluster {
		switch svcObj.Spec.Type {
		case corev1.ServiceTypeLoadBalancer:
			if GlobalSvcIsLb == nil {
				GlobalSvcIsLb = make(map[string]bool)
			}
			GlobalSvcIsLb[newSvcName] = true

			if svcObj.Spec.LoadBalancerIP != "" &&
				svcObj.Annotations[VsVipIgnore] == "true" {
				if GlobalSvcLeaseDetail == nil {
					GlobalSvcLeaseDetail = make(map[string]string)
				}
				GlobalSvcLeaseDetail[newSvcName] = findLeaderBaseLbip(svcObj.Spec.LoadBalancerIP)
			}

		default:
			if GlobalSvcIsLb == nil {
				GlobalSvcIsLb = make(map[string]bool)
			}
			GlobalSvcIsLb[newSvcName] = false
		}
	} else {
		if GlobalSvcIsLb == nil {
			GlobalSvcIsLb = make(map[string]bool)
		}
		GlobalSvcIsLb[newSvcName] = false
	}
}
