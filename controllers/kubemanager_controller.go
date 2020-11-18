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

package controllers

import (
	"context"
	"fmt"
	"juniper.net/kube-manager/mcmanager"
	ctrl "sigs.k8s.io/controller-runtime"
	"time"

	"admiralty.io/multicluster-controller/pkg/reconcile"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/reference"
	"sigs.k8s.io/controller-runtime/pkg/client"

	contrailCorev1alpha1 "juniper.net/contrail/api/v1alpha1"
	"juniper.net/contrail/lib/resource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	kubeManagerVirtualMachineFinalizer = "virtual-machine.finalizers.kube-manager.juniper.net/contrail"
	vmUIDAnnotation                    = "kube-manager.juniper.net/vm-uuid"
)

// KubeManagerReconciler reconciles a KubeManager object
type KubeManagerReconciler struct {
	Manager *mcmanager.MultiClusterManager
	Log     logr.Logger
	Scheme  *runtime.Scheme
}

/*
Reconcile Kubernetes Pods resources with Contrail resources

Reconciliation steps:

 1. read the Pod from the cache (ignore pod using host network)
 2. set a Pod finalizer to ensure deletion of the Contrail VM when Pod is deleted
 3. ensure dedicated VM
 4. for all Pod network (for the moment only the KM default network)
	a. ensure VMI with VM as controller owner on the network for the VM
	b. ensure IIP with VM as controller owner for the VMI on the same VN
 5. if pod is scheduled on a node, set a reference from the node's vrouter to
    the pod's VM
*/
// +kubebuilder:rbac:groups=kube-manager.juniper.net,resources=kubemanagers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kube-manager.juniper.net,resources=kubemanagers/status,verbs=get;update;patch
func (r *KubeManagerReconciler) Reconcile(req reconcile.Request) (reconcile.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("kubemanager", req.NamespacedName)
	log.Info(fmt.Sprintf("reconciling %s / %s / %s", req.Context, req.Namespace, req.Name),
		"context", req.Context, "namespace", req.Namespace, "name", req.Name)

	// TODO(daled): Add cluster context to pod ID. Config NG resources are still only
	//  using Namespace and Name to identify pods. Now that pods can reside on
	//  multiple clusters, Namespace and Name are not guaranteed to be unique.
	//  https://ssd-git.juniper.net/contrail/contrail-config-ng/-/issues/46

	dc, ok := r.Manager.GetDelegatingClient(req.Context)
	if !ok {
		log.Info(
			fmt.Sprintf("no delegating client for %s cluster", req.Context),
			"context", req.Context)
		return reconcile.Result{}, nil
	}
	// return empty result and no error

	pod := &corev1.Pod{}
	if err := dc.Get(ctx, req.NamespacedName, pod); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Pod using host network (usually system one) are not managed by Contrail,
	// ignore them
	if pod.Spec.HostNetwork {
		log.V(1).Info("pod using host network, ignore it")
		return reconcile.Result{}, nil
	}
	log.V(1).Info("reconciling Pod", "Spec", pod.Spec)

	// if Pod is removing, delete corresponding VM. As VM is controller owner
	// of other Contrail resource, garbage collector will take care of the
	// deletion.
	if finalized, err := r.finalize(dc, ctx, pod); err != nil {
		return reconcile.Result{}, err
	} else if finalized {
		return reconcile.Result{}, nil
	}
	if err := resource.EnsureFinalizer(
		ctx, dc, pod, kubeManagerVirtualMachineFinalizer); err != nil {
		return reconcile.Result{}, err
	}

	vm, err := r.ensureVirtualMachine(ctx, pod)
	if err != nil {
		return reconcile.Result{}, err
	}
	if vm.Status.State == contrailCorev1alpha1.FailureState {
		return reconcile.Result{}, fmt.Errorf("VirtualMachine in fail to reconcile: %s", vm.Status.Observation)
	}
	if vm.Status.State == contrailCorev1alpha1.PendingState {
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}

	// set the pod's VM UUID ans an pod annotation used by the Contrail CNI
	annotations := pod.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[vmUIDAnnotation] = string(vm.UID)
	pod.SetAnnotations(annotations)
	if err := dc.Update(ctx, pod); err != nil {
		return reconcile.Result{}, err
	}

	vmi, err := r.ensureVirtualMachineInterface(ctx, pod, vm)
	if err != nil {
		return reconcile.Result{}, err
	}
	if vmi.Status.State == contrailCorev1alpha1.FailureState {
		return reconcile.Result{}, fmt.Errorf("VirtualMachineInterface failed to reconcile: %s",
			vmi.Status.Observation)
	}
	if vmi.Status.State == contrailCorev1alpha1.PendingState {
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}

	iip, err := r.ensureInstanceIP(ctx, pod, vm, vmi)
	if err != nil {
		return reconcile.Result{}, err
	}
	if iip.Status.State == contrailCorev1alpha1.FailureState {
		return reconcile.Result{}, fmt.Errorf("InstanceIP failed to reconcile: %s", iip.Status.Observation)
	}
	if iip.Status.State == contrailCorev1alpha1.PendingState {
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}

	// Set the ref from node's VR to pod's VM when pod is scheduled
	if err := r.ensureVirtualRouterReference(ctx, pod, vm); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *KubeManagerReconciler) finalize(dc *client.DelegatingClient, ctx context.Context, pod *corev1.Pod) (bool, error) {
	if deleted, err := resource.HandleFinalizing(
		ctx, dc, pod, kubeManagerVirtualMachineFinalizer, func() error {
			return r.deleteVirtualMachine(ctx, pod)
		}); err != nil {
		return false, err
	} else if !deleted {
		return false, nil
	}

	return true, nil
}

func (r *KubeManagerReconciler) deleteVirtualMachine(ctx context.Context, pod *corev1.Pod) error {
	log := r.Log.WithName("kubemanager")
	vm := &contrailCorev1alpha1.VirtualMachine{}
	c := r.Manager.GetCachingConfigClusterClient()
	if err := c.Get(ctx, types.NamespacedName{Name: pod.Name}, vm); err != nil {
		return client.IgnoreNotFound(err)
	}
	// TODO(edouard): we can create an index for VR based on the IP address
	// and/or hostname to find directly chosen VR
	log.V(1).Info("VirtualMachine.Kind", "Kind", vm.Kind)
	vrs := &contrailCorev1alpha1.VirtualRouterList{}
	k, v := contrailCorev1alpha1.GetBackReferenceLabelFromObject(vm.Kind, vm)
	opts := client.MatchingLabels(map[string]string{k: v})
	if err := c.List(ctx, vrs, opts); err != nil {
		return errors.Wrap(err, "unable to delete VirtualMachine")
	}
vrLoop:
	for _, vr := range vrs.Items {
		for idx, vrVMRef := range vr.Spec.VirtualMachineReferences {
			if vrVMRef.Name != vm.Name {
				continue
			}
			// TODO(edouard): cannot use VR item for the update later?
			fetchedVR := &contrailCorev1alpha1.VirtualRouter{}
			if err := c.Get(ctx, types.NamespacedName{Name: vr.Name}, fetchedVR); err != nil {
				return errors.Wrap(err, "unable to delete VirtualMachine")
			}
			fetchedVR.Spec.VirtualMachineReferences = append(
				vr.Spec.VirtualMachineReferences[:idx],
				vr.Spec.VirtualMachineReferences[idx+1:]...,
			)
			// ref removed, need to update the VR
			if err := c.Update(ctx, fetchedVR); err != nil {
				return errors.Wrap(err, "unable to delete VirtualMachine")
			}
			break vrLoop
		}
		break vrLoop
	}

	if err := c.Delete(ctx, vm, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
		return err
	}
	return nil
}

func (r *KubeManagerReconciler) ensureVirtualMachine(
	ctx context.Context, pod *corev1.Pod) (*contrailCorev1alpha1.VirtualMachine, error) {
	vm := &contrailCorev1alpha1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			// TODO(edouard): pod name is not unique, we should use ns + name
			// for unicity, but that cannot fit in name length restriction
			Name: pod.Name,
		},
	}

	if action, err := ctrl.CreateOrUpdate(ctx, r.Manager.GetCachingConfigClusterClient(), vm, func() error {
		vm.Spec.ServerType = contrailCorev1alpha1.Container
		return nil
	}); err != nil {
		return vm, errors.Wrapf(err, "%s VirtualMachine fail", action)
	}
	return vm, nil
}

func (r *KubeManagerReconciler) ensureVirtualMachineInterface(
	ctx context.Context, pod *corev1.Pod, vm *contrailCorev1alpha1.VirtualMachine) (
	*contrailCorev1alpha1.VirtualMachineInterface, error) {
	vn := r.Manager.GetVirtualNetwork()
	vmi := &contrailCorev1alpha1.VirtualMachineInterface{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: vn.Namespace,
			// TODO(edouard): pod name is not unique, we should use ns + name
			// for unicity, but that cannot fit in name length restriction
			Name: pod.Name,
		},
	}

	if action, err := ctrl.CreateOrUpdate(ctx, r.Manager.GetCachingConfigClusterClient(), vmi, func() error {
		// set the VM as controller owner of the VMI
		if err2 := ctrl.SetControllerReference(vm, vmi, r.Scheme); err2 != nil {
			return errors.Wrap(err2,
				"unable to set VirtualMachine as controller owner of VirtualMachineInterface")
		}

		// annotations used by Contrail CNI
		annotations := vmi.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["kind"] = "Pod"
		project := r.Manager.GetProject()
		annotations["namespace"] = project.Name
		annotations["name"] = pod.Name
		annotations["network"] = "default"
		annotations["index"] = "0/1"
		vmi.SetAnnotations(annotations)

		// Project parent ref
		parentRef, err2 := reference.GetReference(r.Scheme, project)
		if err2 != nil {
			return errors.Wrap(err2, "unable to set Project parent reference")
		}
		vmi.Spec.Parent = *parentRef

		// VN ref
		vnRef, err2 := reference.GetReference(r.Scheme, vn)
		if err2 != nil {
			return errors.Wrap(err2, "unable to set VirtualNetwork references")
		}
		vmi.Spec.VirtualNetworkReferences = []contrailCorev1alpha1.ResourceReference{
			{
				ObjectReference: *vnRef,
			},
		}

		// VM ref
		vmRef, err2 := reference.GetReference(r.Scheme, vm)
		if err2 != nil {
			return errors.Wrap(err2, "unable to set VirtualMachine references")
		}
		vmi.Spec.VirtualMachineReferences = []contrailCorev1alpha1.ResourceReference{
			{
				ObjectReference: *vmRef,
			},
		}

		return nil
	}); err != nil {
		return vmi, errors.Wrapf(err, "%s VirtualMachine fail", action)
	}
	return vmi, nil
}

func (r *KubeManagerReconciler) ensureInstanceIP(
	ctx context.Context,
	pod *corev1.Pod,
	vm *contrailCorev1alpha1.VirtualMachine,
	vmi *contrailCorev1alpha1.VirtualMachineInterface) (
	*contrailCorev1alpha1.InstanceIP, error) {
	iip := &contrailCorev1alpha1.InstanceIP{
		ObjectMeta: metav1.ObjectMeta{
			// TODO(edouard): pod name is not unique, we should use ns + name
			// for unicity, but that cannot fit in name length restriction
			Name: pod.Name,
		},
	}

	if action, err := ctrl.CreateOrUpdate(ctx, r.Manager.GetCachingConfigClusterClient(), iip, func() error {
		// set the VM as controller owner of the IIP
		if err2 := ctrl.SetControllerReference(vm, iip, r.Scheme); err2 != nil {
			return errors.Wrap(err2,
				"unable to set VirtualMachine as controller owner of InstanceIP")
		}

		// VN ref
		vnRef, err2 := reference.GetReference(r.Scheme, r.Manager.GetVirtualNetwork())
		if err2 != nil {
			return errors.Wrap(err2, "unable to set InstanceIP's VirtualNetwork references")
		}
		iip.Spec.VirtualNetworkReferences = []contrailCorev1alpha1.ResourceReference{
			{
				ObjectReference: *vnRef,
			},
		}

		// VMI ref
		vmiRef, err2 := reference.GetReference(r.Scheme, vmi)
		if err2 != nil {
			return errors.Wrap(err2, "unable to set InstanceIP's VirtualMachineInterface references")
		}
		iip.Spec.VirtualMachineInterfaceReferences = []contrailCorev1alpha1.ResourceReference{
			{
				ObjectReference: *vmiRef,
			},
		}

		return nil
	}); err != nil {
		return iip, errors.Wrapf(err, "%s InstanceIP fail", action)
	}
	return iip, nil
}

func (r *KubeManagerReconciler) ensureVirtualRouterReference(
	ctx context.Context,
	pod *corev1.Pod,
	vm *contrailCorev1alpha1.VirtualMachine) error {

	if pod.Spec.NodeName == "" || pod.Status.HostIP == "" {
		// pod not yet scheduled, wait next reconciliation request
		return nil
	}
	c := r.Manager.GetCachingConfigClusterClient()

	// TODO(edouard): we can create an index for VR based on the IP address
	// and/or hostname to find directly chosen VR
	vrs := &contrailCorev1alpha1.VirtualRouterList{}
	if err := c.List(ctx, vrs); err != nil {
		return errors.Wrap(err, "unable to set VirtualRouter's VirtualMachine references")
	}
vrLoop:
	for _, vr := range vrs.Items {
		if string(vr.Spec.IPAddress) != pod.Status.HostIP {
			continue
		}
		// TODO(edouard): cannot use VR item for the update later?
		fetchedVR := &contrailCorev1alpha1.VirtualRouter{}
		if err := c.Get(ctx, types.NamespacedName{Name: vr.Name}, fetchedVR); err != nil {
			return errors.Wrap(err, "unable to set VirtualRouter's VirtualMachine references")
		}
		vmRef, err := reference.GetReference(r.Scheme, vm)
		if err != nil {
			return errors.Wrap(err, "unable to set VirtualRouter's VirtualMachine references")
		}
		vrVMRefs := fetchedVR.Spec.VirtualMachineReferences
		// first VM ref, add it
		if vrVMRefs == nil {
			fetchedVR.Spec.VirtualMachineReferences = []contrailCorev1alpha1.ResourceReference{
				{
					ObjectReference: *vmRef,
				},
			}
		} else {
			for _, ref := range vrVMRefs {
				if ref.Name == vm.Name {
					// already there, stop here, no update needed
					break vrLoop
				}
			}
			// not there, append it
			fetchedVR.Spec.VirtualMachineReferences = append(
				fetchedVR.Spec.VirtualMachineReferences,
				contrailCorev1alpha1.ResourceReference{
					ObjectReference: *vmRef,
				},
			)
		}
		// ref added, need to update the VR
		if err := c.Update(ctx, fetchedVR); err != nil {
			return errors.Wrap(err, "unable to set VirtualRouter's VirtualMachine references")
		}
		// VR found, stop here
		break vrLoop
	}
	return nil
}
