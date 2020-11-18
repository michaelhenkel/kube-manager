/*
Copyright 2020 Juniper Networks.

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
	"context"
	"flag"
	"juniper.net/kube-manager/mcmanager"
	"os"

	"github.com/kelseyhightower/envconfig"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/sample-controller/pkg/signals"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	contrailCorev1alpha1 "juniper.net/contrail/api/v1alpha1"
	kmc "juniper.net/kube-manager/controllers"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = contrailCorev1alpha1.AddToScheme(scheme)

	// +kubebuilder:scaffold:scheme
}

// Log an error and exit with code 1
func logAndDie(err error, msg string, keysAndValues ...interface{}) {
	if keysAndValues != nil {
		setupLog.Error(err, msg, keysAndValues)
	} else {
		setupLog.Error(err, msg)
	}
	os.Exit(1)
}

func main() {
	stopCh := signals.SetupSignalHandler()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		cancel()
	}()

	var metricsAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-addr", ":8081", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	settings := &mcmanager.KubeManagerConfiguration{}
	if err := envconfig.Process("kubemanager", settings); err != nil {
		logAndDie(err, "unable to get kubemanager configuration")
	}

	options := ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		Port:               9443,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "21855179.juniper.net",
	}

	// Create manager for Contrail config cluster
	config := ctrl.GetConfigOrDie()

	reconciler := &kmc.KubeManagerReconciler{
		Log:    ctrl.Log.WithName("controllers").WithName("KubeManager"),
		Scheme: options.Scheme,
	}

	var err error
	reconciler.Manager, err = mcmanager.NewMultiClusterManager(settings, config, options, ctx, reconciler)
	if err != nil {
		logAndDie(err, "unable to create multi-cluster managers")
	}
	if err := reconciler.Manager.Start(stopCh); err != nil {
		if err.ConfigClusterError != nil {
			setupLog.Error(err.ConfigClusterError, "problem running config manager")
		}
		if err.ManagedClusterError != nil {
			setupLog.Error(err.ManagedClusterError, "problem running multi-cluster manager")
		}
		os.Exit(1)
	}
}
