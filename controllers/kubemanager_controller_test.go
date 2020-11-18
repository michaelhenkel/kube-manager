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
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/reference"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"

	corev1alpha1 "juniper.net/contrail/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func containsReference(refs []corev1alpha1.ResourceReference, ref *corev1.ObjectReference) bool {
	for _, r := range refs {
		if r.APIVersion == ref.APIVersion &&
			r.Kind == ref.Kind &&
			r.Namespace == ref.Namespace &&
			r.Name == ref.Name &&
			r.UID == ref.UID {
			return true
		}
	}
	return false
}

var _ = Describe("Contrail Kube Manager", func() {
	Context("create Pod with host network", func() {
		It("should ignore the Pod", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    corev1.NamespaceDefault,
					GenerateName: "test-pod-",
				},
				Spec: corev1.PodSpec{
					HostNetwork: true,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "k8s.gcr.io/echoserver:1.10",
						},
					},
				},
			}
			err := podClient.Create(context.Background(), pod)
			Expect(err).ToNot(HaveOccurred())

			vm := &corev1alpha1.VirtualMachine{}
			key := client.ObjectKey{Name: pod.Name}
			for i := 0; i < 3; i++ {
				err := k8sClient.Get(context.Background(), key, vm)
				Expect(apierrors.IsNotFound(err)).To(BeTrue())
				time.Sleep(time.Millisecond * 10)
			}
		})
	})

	Context("create Pod without host network", func() {
		It("should create and delete a dedicated VirtualMachine", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    corev1.NamespaceDefault,
					GenerateName: "test-pod-",
				},
				Spec: corev1.PodSpec{
					HostNetwork: false,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "k8s.gcr.io/echoserver:1.10",
						},
					},
				},
			}
			err := podClient.Create(context.Background(), pod)
			Expect(err).ToNot(HaveOccurred())

			vm := &corev1alpha1.VirtualMachine{}
			key := client.ObjectKey{Name: pod.Name}
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), key, vm)
				return vm.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))
			Expect(vm.Spec.ServerType).To(Equal(corev1alpha1.Container))

			if useExistingCluster() {
				err = podClient.Delete(context.Background(), pod)
				Expect(err).ToNot(HaveOccurred())

				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), key, vm)
					return apierrors.IsNotFound(err)
				}, timeout, interval).Should(BeTrue())
			}
		})

		It("should create and delete a dedicated VirtualMachineInterface", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    corev1.NamespaceDefault,
					GenerateName: "test-pod-",
				},
				Spec: corev1.PodSpec{
					HostNetwork: false,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "k8s.gcr.io/echoserver:1.10",
						},
					},
				},
			}
			err := podClient.Create(context.Background(), pod)
			Expect(err).ToNot(HaveOccurred())

			vm := &corev1alpha1.VirtualMachine{}
			key := client.ObjectKey{Name: pod.Name}
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), key, vm)
				return vm.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))

			vmi := &corev1alpha1.VirtualMachineInterface{}
			key = client.ObjectKey{
				Namespace: project.Name,
				Name:      pod.Name,
			}
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), key, vmi)
				return vmi.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))
			True := true
			expectedOwnerReference := metav1.OwnerReference{
				Kind:               "VirtualMachine",
				APIVersion:         "core.contrail.juniper.net/v1alpha1",
				UID:                vm.UID,
				Name:               vm.Name,
				Controller:         &True,
				BlockOwnerDeletion: &True,
			}
			Expect(vmi.GetOwnerReferences()).To(ContainElement(expectedOwnerReference))
			Expect(vmi.GetAnnotations()).To(MatchKeys(IgnoreExtras, Keys{
				"kind":      Equal("Pod"),
				"namespace": Equal(project.Name),
				"name":      Equal(pod.Name),
				"network":   Equal("default"),
				"index":     Equal("0/1"),
			}))
			Expect(vmi.Spec.Parent.Kind).To(Equal(projectRef.Kind))
			Expect(vmi.Spec.Parent.Name).To(Equal(projectRef.Name))

			Expect(vmi.Spec.VirtualNetworkReferences[0].Namespace).To(Equal(vn.Namespace))
			Expect(vmi.Spec.VirtualNetworkReferences[0].Name).To(Equal(vn.Name))
			Expect(vmi.Spec.VirtualMachineReferences[0].Namespace).To(Equal(vm.Namespace))
			Expect(vmi.Spec.VirtualMachineReferences[0].Name).To(Equal(vm.Name))

			if useExistingCluster() {
				err = podClient.Delete(context.Background(), pod)
				Expect(err).ToNot(HaveOccurred())

				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), key, vmi)
					return apierrors.IsNotFound(err)
				}, timeout, interval).Should(BeTrue())
			}
		})

		It("should create and delete a dedicated InstanceIP per VirtualMachineInterface", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    corev1.NamespaceDefault,
					GenerateName: "test-pod-",
				},
				Spec: corev1.PodSpec{
					HostNetwork: false,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "k8s.gcr.io/echoserver:1.10",
						},
					},
				},
			}
			err := podClient.Create(context.Background(), pod)
			Expect(err).ToNot(HaveOccurred())

			vm := &corev1alpha1.VirtualMachine{}
			key := client.ObjectKey{Name: pod.Name}
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), key, vm)
				return vm.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))

			vmi := &corev1alpha1.VirtualMachineInterface{}
			key = client.ObjectKey{
				Namespace: project.Name,
				Name:      pod.Name,
			}
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), key, vmi)
				return vmi.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))

			iip := &corev1alpha1.InstanceIP{}
			key = client.ObjectKey{
				Name: pod.Name,
			}
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), key, iip)
				return iip.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))
			True := true
			expectedOwnerReference := metav1.OwnerReference{
				Kind:               "VirtualMachine",
				APIVersion:         "core.contrail.juniper.net/v1alpha1",
				UID:                vm.UID,
				Name:               vm.Name,
				Controller:         &True,
				BlockOwnerDeletion: &True,
			}
			Expect(iip.GetOwnerReferences()).To(ContainElement(expectedOwnerReference))
			Expect(iip.Spec.VirtualNetworkReferences[0].Namespace).To(Equal(vn.Namespace))
			Expect(iip.Spec.VirtualNetworkReferences[0].Name).To(Equal(vn.Name))
			Expect(iip.Spec.VirtualMachineInterfaceReferences[0].Namespace).To(Equal(vmi.Namespace))
			Expect(iip.Spec.VirtualMachineInterfaceReferences[0].Name).To(Equal(vmi.Name))

			if useExistingCluster() {
				err = podClient.Delete(context.Background(), pod)
				Expect(err).ToNot(HaveOccurred())

				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), key, vmi)
					return apierrors.IsNotFound(err)
				}, timeout, interval).Should(BeTrue())
			}
		})

		It("should add/remove ref from Pod's VirtualRouter", func() {
			hostIP := "100.100.100.100"
			vr := &corev1alpha1.VirtualRouter{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-virtual-router-",
				},
				Spec: corev1alpha1.VirtualRouterSpec{
					IPAddress: corev1alpha1.IPAddress(hostIP),
				},
			}
			Expect(k8sClient.Create(context.Background(), vr)).Should(Succeed())
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), client.ObjectKey{Name: vr.Name}, vr)
				return vr.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    corev1.NamespaceDefault,
					GenerateName: "test-pod-",
				},
				Spec: corev1.PodSpec{
					HostNetwork: false,
					NodeName:    vr.Name,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "k8s.gcr.io/echoserver:1.10",
						},
					},
				},
				Status: corev1.PodStatus{
					HostIP: hostIP,
				},
			}
			err := podClient.Create(context.Background(), pod)
			Expect(err).ToNot(HaveOccurred())

			vm := &corev1alpha1.VirtualMachine{}
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), client.ObjectKey{Name: pod.Name}, vm)
				return vm.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))
			vmRef, err := reference.GetReference(scheme.Scheme, vm)
			Expect(err).NotTo(HaveOccurred())

			err = podClient.Get(context.Background(), client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, pod)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() string {
				pod.Status.HostIP = hostIP
				_ = podClient.Status().Update(context.Background(), pod)
				return pod.Status.HostIP
			}, timeout, interval).Should(Equal(hostIP))

			Eventually(func() []corev1alpha1.ResourceReference {
				_ = k8sClient.Get(context.Background(), client.ObjectKey{Name: vr.Name}, vr)
				return vr.Spec.VirtualMachineReferences
			}, timeout, interval).ShouldNot(BeEmpty())
			Expect(containsReference(vr.Spec.VirtualMachineReferences, vmRef)).To(BeTrue())

			err = podClient.Delete(context.Background(), pod)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() bool {
				_ = k8sClient.Get(context.Background(), client.ObjectKey{Name: vr.Name}, vr)
				return containsReference(vr.Spec.VirtualMachineReferences, vmRef)
			}, timeout, interval).Should(BeFalse())
		})

		It("should set the VirtualMachine UUID in the Pod annotations", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    corev1.NamespaceDefault,
					GenerateName: "test-pod-",
				},
				Spec: corev1.PodSpec{
					HostNetwork: false,
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "k8s.gcr.io/echoserver:1.10",
						},
					},
				},
			}
			err := podClient.Create(context.Background(), pod)
			Expect(err).ToNot(HaveOccurred())

			vm := &corev1alpha1.VirtualMachine{}
			key := client.ObjectKey{Name: pod.Name}
			Eventually(func() corev1alpha1.StateType {
				_ = k8sClient.Get(context.Background(), key, vm)
				return vm.Status.State
			}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))
			Expect(vm.Spec.ServerType).To(Equal(corev1alpha1.Container))

			key = client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}
			Eventually(func() string {
				_ = podClient.Get(context.Background(), key, pod)
				a, _ := pod.GetAnnotations()[vmUIDAnnotation]
				return a
			}, timeout, interval).Should(Equal(string(vm.UID)))
		})
	})
})
