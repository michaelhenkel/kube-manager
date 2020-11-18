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
package mcmanager

import (
	"admiralty.io/multicluster-controller/pkg/cluster"
	"admiralty.io/multicluster-controller/pkg/controller"
	"admiralty.io/multicluster-controller/pkg/manager"
	"admiralty.io/multicluster-controller/pkg/reconcile"
	"admiralty.io/multicluster-service-account/pkg/config"
	"context"
	"fmt"
	"github.com/go-logr/logr"
	contrailCorev1alpha1 "juniper.net/contrail/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crm "sigs.k8s.io/controller-runtime/pkg/manager"
)

// The MultiClusterManager houses config cluster manager, the managed cluster
// manager and delegating client map, settings, config `Project` and
// `VirtualNetwork`, etc. It provides a single `Start` method that starts and
// blocks on both the config cluster and managed cluster managers.
type MultiClusterManager struct {
	dc       map[string]*client.DelegatingClient
	settings *KubeManagerConfiguration
	cm       crm.Manager
	mm       *manager.Manager
	project  *contrailCorev1alpha1.Project
	vn       *contrailCorev1alpha1.VirtualNetwork
	cr       *ConfigReconciler
	log      logr.Logger
}

// Create a new, initialized `MultiClusterManager` ready to `Start`.
func NewMultiClusterManager(
	settings *KubeManagerConfiguration,
	config *rest.Config,
	options crm.Options,
	ctx context.Context,
	reconciler reconcile.Reconciler) (*MultiClusterManager, error) {

	// Create the single-cluster `Manager` for config.
	mgr, err := ctrl.NewManager(config, options)
	if err != nil {
		return nil, err
	}
	log := ctrl.Log.WithName("manager").WithName("MultiClusterManager")
	c := mgr.GetAPIReader()
	l := log.WithName("NewMultiClusterManager")

	// Verify `ClusterProject` exists.
	project := &contrailCorev1alpha1.Project{}
	if err := c.Get(ctx, types.NamespacedName{Name: settings.ClusterProject}, project); err != nil {
		l.Error(err, fmt.Sprintf("cannot locate Project %s", settings.ClusterProject))
		return nil, err
	}

	// Verify `ClusterVirtualNetwork` exists and is in `SuccessState`.
	vn := &contrailCorev1alpha1.VirtualNetwork{}
	key := types.NamespacedName{
		Namespace: project.Name,
		Name:      settings.ClusterVirtualNetwork,
	}
	if err := c.Get(ctx, key, vn); err != nil {
		log.Error(err, fmt.Sprintf("cannot locate VirtualNetwork %s", settings.ClusterVirtualNetwork))
		return nil, err
	}
	if vn.Status.State != contrailCorev1alpha1.SuccessState {
		return nil, fmt.Errorf("configured VirtualNetwork %s not ready: %s because %s",
			vn.Name, vn.Status.State, vn.Status.Observation)
	}

	// Create single-cluster controller for config `Manager`.
	cr := &ConfigReconciler{
		Log: log,
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&contrailCorev1alpha1.Project{}).
		For(&contrailCorev1alpha1.VirtualNetwork{}).
		Complete(cr); err != nil {
		log.Error(err, fmt.Sprintf("error creating config controller: %v", err))
		return nil, err
	}

	// Create multi-cluster controller and `DelegatingClient` map.
	co := controller.New(reconciler, controller.Options{})
	dc := make(map[string]*client.DelegatingClient)
	mcctxs := unique(settings.ManagedClusterConfigContexts)
	add := func(kubeCtx string, cfg *rest.Config) error {
		cl, d, err := addCluster(kubeCtx, cfg)
		if err != nil {
			return err
		}
		dc[cl.Name] = d
		if err := co.WatchResourceReconcileObject(
			ctx, cl, &corev1.Pod{}, controller.WatchOptions{}); err != nil {
			return err
		}
		return nil
	}

	// When no ManagedClusterConfigContexts is provided, use the default KUBECONFIG
	// context. This cluster will be named "default", and its `DelegatingClient`
	// can be retrieved via that name.
	// https://ssd-git.juniper.net/contrail/contrail-config-ng/-/issues/45/
	if len(mcctxs) < 1 {
		err := add("default", config)
		if err != nil {
			return nil, err
		}
	} else {
		for _, kubeCtx := range unique(settings.ManagedClusterConfigContexts) {
			dcctx, err := getConfig(kubeCtx)
			if err != nil {
				return nil, fmt.Errorf("unable to get named config for cluster context %s", kubeCtx)
			}
			err = add(kubeCtx, dcctx)
			if err != nil {
				return nil, err
			}
		}
	}

	// Create multi-cluster `Manager` and return new `MultiClusterManager`.
	mm := manager.New()
	mm.AddController(co)
	mcm := &MultiClusterManager{
		dc:       dc,
		settings: settings,
		cm:       mgr,
		mm:       mm,
		project:  project,
		vn:       vn,
		cr:       cr,
		log:      log,
	}
	return mcm, nil
}

// Start starts all registered Controllers for both the Config cluster manager
// and the managed cluster manager, then blocks until the Stop channel is closed.
// Returns an error if there is an error starting any controller.
func (m *MultiClusterManager) Start(stop <-chan struct{}) *MultiClusterError {
	chConfig := make(chan error, 1)
	chManaged := make(chan error, 1)

	go func() {
		chConfig <- m.cm.Start(stop)
	}()
	go func() {
		chManaged <- m.mm.Start(stop)
	}()

	errConfig := <-chConfig
	errManaged := <-chManaged

	return fmtError(errConfig, errManaged)
}

// Get the `KubeManagerConfiguration`.
func (m *MultiClusterManager) GetSettings() *KubeManagerConfiguration {
	return m.settings
}

// Get the `DelegatingClient` for the specified `clusterContext` (i.e. cluster name).
func (m *MultiClusterManager) GetDelegatingClient(clusterContext string) (*client.DelegatingClient, bool) {
	c, ok := m.dc[clusterContext]
	return c, ok
}

// Get the config cluster REST client.
func (m *MultiClusterManager) GetCachingConfigClusterClient() client.Client {
	if m.cm != nil {
		return m.cm.GetClient()
	}
	return nil
}

// Get the `ClusterVirtualNetwork`.
func (m *MultiClusterManager) GetVirtualNetwork() *contrailCorev1alpha1.VirtualNetwork {
	return m.vn
}

// Get the `ClusterProject`.
func (m *MultiClusterManager) GetProject() *contrailCorev1alpha1.Project {
	return m.project
}

// Create a copy of `strSlice` with duplicates removed.
func unique(strSlice []string) []string {
	keys := make(map[string]bool)
	var list []string
	for _, entry := range strSlice {
		if _, value := keys[entry]; !value {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

// Get the named `rest.Config`.
func getConfig(kubeCtx string) (*rest.Config, error) {
	var cfg *rest.Config
	var err error
	cfg, _, err = config.NamedConfigAndNamespace(kubeCtx)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// Add a new Cluster and DelegatingClient for the given cluster context.
func addCluster(name string, cfg *rest.Config) (*cluster.Cluster, *client.DelegatingClient, error) {
	cl := cluster.New(name, cfg, cluster.Options{})
	dc, err := cl.GetDelegatingClient()
	if err != nil {
		return cl, nil, fmt.Errorf("unable to get delegating client for %s cluster", cl.Name)
	}
	return cl, dc, nil
}
