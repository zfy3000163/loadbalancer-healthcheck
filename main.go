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

package main

import (
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"k8s.io/klog/v2"

	lbhcv1 "cloud.edge/lbhc-controller/apis/lbhc/v1"
	"cloud.edge/lbhc-controller/controllers/lbhc"
	lbhccontrollers "cloud.edge/lbhc-controller/controllers/lbhc"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(lbhcv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string

	var hostName string
	hostName = os.Getenv("KUBE_NODE_NAME")
	hostName = hostName + "-lbhc-sample"

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&lbhc.CrdName, "crd-name", hostName, "The crd's name.")
	flag.StringVar(&lbhc.CrdNamespace, "crd-namespace", "hci-network-system", "The crd's namespace.")
	flag.StringVar(&lbhc.NgxServiceAddress, "ngx-address", "http://127.0.0.1", "The ngx's address ip.")
	flag.StringVar(&lbhc.NgxServicePort, "ngx-port", "5010", "The ngx's port.")
	flag.StringVar(&lbhc.NgxServiceHcUrl, "ngx-hc-url", "down", "The ngx's hc url.")
	flag.BoolVar(&lbhc.DeployIsCluster, "dep-is-cluster", true, "Deployment's env is Cluster.")
	flag.DurationVar(&lbhc.SyncFrequency, "sync-frequency", 3, "The sync frequency.")
	flag.DurationVar(&lbhc.WaitSetDownFrequency, "waitSetDown-frequency", 3, "The waitSetDown frequency.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	klog.InitFlags(flag.CommandLine)

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	config := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     "0", //metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: "0", //probeAddr,

		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "053a3108.cloud.edge",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	kubeclient, _ := kubernetes.NewForConfig(config)

	if err = (&lbhccontrollers.LbhcReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Kubeclient: kubeclient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Lbhc")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("host starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
