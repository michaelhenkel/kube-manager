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
	"juniper.net/kube-manager/mcmanager"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	ref "k8s.io/client-go/tools/reference"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	crm "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	// +kubebuilder:scaffold:imports

	corev1alpha1 "juniper.net/contrail/api/v1alpha1"
	contrailctrl "juniper.net/contrail/controllers"
	"juniper.net/contrail/lib/idallocator"
	"juniper.net/contrail/webhooks"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var podClient client.Client
var testEnv *envtest.Environment
var project *corev1alpha1.Project
var projectRef *v1.ObjectReference
var vn *corev1alpha1.VirtualNetwork

const (
	// Use external k8s cluster API
	envUseExistingCluster = "USE_EXISTING_CLUSTER"
	// envtest should starts controllers
	envUseExistingControllers = "USE_EXISTING_CONTROLLERS"
	// timeout before failing on condition on reading/listing resource from k8s API
	timeout = time.Second * 30
	// retrying interval use to query k8s API
	interval = time.Second * 1
)

type object interface {
	metav1.Object
	runtime.Object
}

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

func useExistingCluster() bool {
	return strings.ToLower(os.Getenv(envUseExistingCluster)) == "true"
}

func useExistingControllers() bool {
	return strings.ToLower(os.Getenv(envUseExistingControllers)) == "true"
}

var _ = BeforeSuite(func(done Done) {
	logf.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(GinkgoWriter)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "config", "crd", "bases"),
			filepath.Join("..", "..", "contrail", "config", "crd", "bases"),
		},
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			DirectoryPaths: []string{
				filepath.Join("..", "..", "contrail", "config", "webhook"),
			},
		},
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	err = clientgoscheme.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = corev1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	// starting required Contrail config controller in a separate manager
	if !useExistingCluster() || !useExistingControllers() {
		configManager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:             scheme.Scheme,
			MetricsBindAddress: ":0",
			Port:               testEnv.WebhookInstallOptions.LocalServingPort,
			Host:               testEnv.WebhookInstallOptions.LocalServingHost,
			CertDir:            testEnv.WebhookInstallOptions.LocalServingCertDir,
		})
		Expect(err).ToNot(HaveOccurred())

		server := configManager.GetWebhookServer()
		server.Register(
			webhooks.CommonValidationWebhookPath,
			&webhook.Admission{Handler: &webhooks.CommonValidateWebhook{
				Log: ctrl.Log.WithName("webhook").WithName("validate"),
			}})
		server.Register(
			webhooks.CommonMutatingWebhookPath,
			&webhook.Admission{Handler: &webhooks.CommonMutateWebhook{
				Client: configManager.GetClient(),
				Log:    ctrl.Log.WithName("webhook").WithName("mutate"),
			}})

		// initiate the ID allocator pool
		pools := &idallocator.Pools{}

		// Add GlobalSystemConfig controller
		err = (&contrailctrl.GlobalSystemConfigReconciler{
			Client: configManager.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("GlobalSystemConfig"),
			Scheme: configManager.GetScheme(),
		}).SetupWithManager(configManager)
		Expect(err).ToNot(HaveOccurred())

		// Add Project controller
		err = (&contrailctrl.ProjectReconciler{
			Client: configManager.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("Project"),
			Scheme: configManager.GetScheme(),
		}).SetupWithManager(configManager)
		Expect(err).ToNot(HaveOccurred())

		// Add VirtualNetwork controller
		err = (&contrailctrl.VirtualNetworkReconciler{
			Client: configManager.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("VirtualNetwork"),
			Scheme: configManager.GetScheme(),
			Pools:  pools,
		}).SetupWithManager(configManager)
		Expect(err).ToNot(HaveOccurred())

		// Add RoutingInstance controller
		err = (&contrailctrl.RoutingInstanceReconciler{
			Client: configManager.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("RoutingInstance"),
			Scheme: configManager.GetScheme(),
			Pools:  pools,
		}).SetupWithManager(configManager)
		Expect(err).ToNot(HaveOccurred())

		// Add VirtualRouter controller
		err = (&contrailctrl.VirtualRouterReconciler{
			Client: configManager.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("VirtualRouter"),
			Scheme: configManager.GetScheme(),
		}).SetupWithManager(configManager)
		Expect(err).ToNot(HaveOccurred())

		// Add VirtualMachineInterface controller
		err = (&contrailctrl.VirtualMachineInterfaceReconciler{
			Client: configManager.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("VirtualMachineInterface"),
			Scheme: configManager.GetScheme(),
		}).SetupWithManager(configManager)
		Expect(err).ToNot(HaveOccurred())

		// Add InstanceIP controller
		err = (&contrailctrl.InstanceIPReconciler{
			Client: configManager.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("InstanceIP"),
			Scheme: configManager.GetScheme(),
			Pools:  pools,
		}).SetupWithManager(configManager)
		Expect(err).ToNot(HaveOccurred())

		go func() {
			defer GinkgoRecover()
			err = configManager.Start(ctrl.SetupSignalHandler())
			Expect(err).ToNot(HaveOccurred())
		}()
	}

	options := ctrl.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: ":0",
	}
	settings := &mcmanager.KubeManagerConfiguration{
		ClusterProject:        "test-project-" + rand.String(5),
		ClusterVirtualNetwork: "test-vn-" + rand.String(5),
	}
	ctx := context.Background()
	var sch *runtime.Scheme

	// provision Contrail resources and start kube-manager manager
	if !useExistingCluster() || !useExistingControllers() {
		var kmManager crm.Manager
		kmManager, err = ctrl.NewManager(cfg, options)
		Expect(err).ToNot(HaveOccurred())
		k8sClient = kmManager.GetClient()

		gsc := &corev1alpha1.GlobalSystemConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: corev1alpha1.DefaultGlobalSystemConfigName,
			},
			Spec: corev1alpha1.GlobalSystemConfigSpec{
				AutonomousSystem: corev1alpha1.DefaultGlobalAutonomousSystem,
			},
		}
		_ = ensureContrailResource(kmManager, gsc)

		reconciler := &KubeManagerReconciler{
			Log:    ctrl.Log.WithName("controllers").WithName("KubeManager"),
			Scheme: kmManager.GetScheme(),
		}

		var err error
		reconciler.Manager, err = mcmanager.NewMultiClusterManager(settings, cfg, options, ctx, reconciler)

		// Project/Namespace, not created should fail to start
		Expect(err).To(HaveOccurred())

		// create a Project/Namespace for the run
		project = &corev1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{
				Name: settings.ClusterProject,
			},
		}
		_ = ensureContrailResource(kmManager, project)

		reconciler.Manager, err = mcmanager.NewMultiClusterManager(settings, cfg, options, ctx, reconciler)

		// still failing to start as VN not created
		Expect(err).To(HaveOccurred())

		// create a VN with a subnet for the run
		projectRef, err = ref.GetReference(scheme.Scheme, project)
		Expect(err).NotTo(HaveOccurred())
		nIPAM := &corev1alpha1.NetworkIPAM{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: settings.ClusterProject,
				Name:      "ipam-" + settings.ClusterVirtualNetwork,
			},
		}
		_ = ensureContrailResource(kmManager, nIPAM)
		nIPAMRef := &corev1alpha1.NetworkIPAMReference{}
		nIPAMObjRef, err := ref.GetReference(scheme.Scheme, nIPAM)
		Expect(err).NotTo(HaveOccurred())
		nIPAMObjRef.DeepCopyInto(&nIPAMRef.ObjectReference)
		nIPAMRef.Attributes = corev1alpha1.NetworkIPAMReferenceAttributes{
			IPAMSubnets: []corev1alpha1.IPAMSubnet{
				{
					Subnet: corev1alpha1.Subnet{
						IPPrefix:       "10.0.0.0",
						IPPrefixLength: intstr.FromInt(24),
					},
					DefaultGateway: "10.0.0.254",
				},
			},
		}
		vn = &corev1alpha1.VirtualNetwork{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: settings.ClusterProject,
				Name:      settings.ClusterVirtualNetwork,
			},
			Spec: corev1alpha1.VirtualNetworkSpec{
				Parent:                *projectRef,
				NetworkIPAMReferences: []corev1alpha1.NetworkIPAMReference{*nIPAMRef},
			},
		}
		_ = ensureContrailResource(kmManager, vn)

		sch = kmManager.GetScheme()
		go func() {
			defer GinkgoRecover()
			err = kmManager.Start(make(chan struct{}))
			// err = kmManager.Start(ctrl.SetupSignalHandler())
			Expect(err).ToNot(HaveOccurred())
		}()

	} else {
		sch = scheme.Scheme
		k8sClient, err = client.New(cfg, client.Options{Scheme: sch})
		Expect(err).ToNot(HaveOccurred())
		Expect(k8sClient).ToNot(BeNil())
	}

	reconciler := &KubeManagerReconciler{
		Log:    ctrl.Log.WithName("controllers").WithName("KubeManager"),
		Scheme: sch,
	}
	reconciler.Manager, err = mcmanager.NewMultiClusterManager(settings, cfg, options, ctx, reconciler)
	Expect(err).NotTo(HaveOccurred())

	var ok bool
	podClient, ok = reconciler.Manager.GetDelegatingClient("default")
	Expect(ok).To(BeTrue())

	// finally start as all needed resource initialized
	go func() {
		defer GinkgoRecover()
		err = reconciler.Manager.Start(make(chan struct{}))
		// err = reconciler.Manager.Start(ctrl.SetupSignalHandler())
		Expect(err).ToNot(HaveOccurred())
	}()

	close(done)
}, 60)

func ensureContrailResource(mgr ctrl.Manager, o object) error {
	c := mgr.GetClient()
	r := mgr.GetAPIReader()
	s := mgr.GetScheme()

	key, err := client.ObjectKeyFromObject(o)
	if err != nil {
		return err
	}

	err = r.Get(context.Background(), key, o)
	if errors.IsNotFound(err) {
		Expect(c.Create(context.Background(), o)).Should(Succeed())
	}

	u := &unstructured.Unstructured{}
	if err := s.Convert(o, u, nil); err != nil {
		return err
	}

	Eventually(func() corev1alpha1.StateType {
		_ = r.Get(context.Background(), key, u)
		val, found, err := unstructured.NestedString(u.UnstructuredContent(), "status", "state")
		if !found || err != nil {
			return ""
		}
		return corev1alpha1.StateType(val)
	}, timeout, interval).Should(Equal(corev1alpha1.SuccessState))

	if err := s.Convert(u, o, nil); err != nil {
		return err
	}

	return nil
}

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})
