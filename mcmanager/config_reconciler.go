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
	"fmt"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime"
)

// ConfigClusterReconciler reconciles Config objects
type ConfigReconciler struct {
	Log logr.Logger
}

// +kubebuilder:rbac:groups=kube-manager.juniper.net,resources=kubemanagers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kube-manager.juniper.net,resources=kubemanagers/status,verbs=get;update;patch
func (r *ConfigReconciler) Reconcile(req controllerruntime.Request) (controllerruntime.Result, error) {
	log := r.Log.WithValues("kubemanager", req.NamespacedName)
	log.Info(fmt.Sprintf("reconciling %s / %s", req.Namespace, req.Name),
		"namespace", req.Namespace, "name", req.Name)
	return controllerruntime.Result{}, nil
}
