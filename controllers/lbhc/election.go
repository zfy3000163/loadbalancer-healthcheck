package lbhc

import (
	"context"
	"os"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

const LbhcLeaderElector = "lbhc-leader-elector"

type Elector struct {
	elector *leaderelection.LeaderElector
}

type leaderElectionConfig struct {
	PodName      string
	PodNamespace string

	Client clientset.Interface

	ElectionID string
	WasLeader  bool

	LeaseDuration int
	RenewDeadline int
	RetryPeriod   int

	OnStartedLeading func()
	OnStoppedLeading func()
	OnNewLeader      func(identity string)
}

func (c *Elector) isLeader() bool {
	return c.elector.IsLeader()
}

func LeaderElection() Elector {
	config := &leaderElectionConfig{
		Client:       Kubeclient,
		ElectionID:   "lbhc",
		PodName:      os.Getenv("POD_NAME"),
		PodNamespace: os.Getenv("KUBE_NAMESPACE"),
	}
	newElector := new(Elector)
	newElector.elector = setupLeaderElection(config)
	return *newElector
}

func setupLeaderElection(config *leaderElectionConfig) *leaderelection.LeaderElector {
	callbacks := leaderelection.LeaderCallbacks{
		OnStartedLeading: func(ctx context.Context) {
			klog.Infof("I am the new leader")
			config.WasLeader = true

			if config.OnStartedLeading != nil {
				config.OnStartedLeading()
			}
		},
		OnStoppedLeading: func() {
			klog.Info("I am not leader anymore")

			if config.OnStoppedLeading != nil {
				config.OnStoppedLeading()
			}
			klog.Fatalf("leaderelection lost")
		},
		OnNewLeader: func(identity string) {
			klog.Infof("new leader elected: %v", identity)
			if config.WasLeader && identity != config.PodName {
				klog.Fatal("I am not leader anymore")
			}
			if config.OnNewLeader != nil {
				config.OnNewLeader(identity)
			}
		},
	}

	broadcaster := record.NewBroadcaster()
	hostname := os.Getenv("KUBE_NODE_NAME")
	recorder := broadcaster.NewRecorder(scheme.Scheme, apiv1.EventSource{
		Component: LbhcLeaderElector,
		Host:      hostname,
	})
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{Namespace: config.PodNamespace, Name: config.ElectionID},
		Client:    config.Client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity:      config.PodName,
			EventRecorder: recorder,
		},
	}
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: 15 * time.Second,
		RenewDeadline: 10 * time.Second,
		RetryPeriod:   2 * time.Second,
		Callbacks:     callbacks,
	})
	if err != nil {
		klog.Fatalf("unexpected error starting leader election: %v", err)
	}

	go elector.Run(context.Background())
	return elector
}
