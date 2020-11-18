module juniper.net/kube-manager

go 1.13

require (
	admiralty.io/multicluster-controller v0.6.0
	admiralty.io/multicluster-service-account v0.6.1
	github.com/go-logr/logr v0.1.0
	github.com/kelseyhightower/envconfig v1.4.0
	github.com/onsi/ginkgo v1.12.1
	github.com/onsi/gomega v1.10.1
	github.com/pkg/errors v0.8.1
	juniper.net/contrail v0.0.0-00010101000000-000000000000
	k8s.io/api v0.18.6
	k8s.io/apimachinery v0.18.6
	k8s.io/client-go v0.18.6
	k8s.io/sample-controller v0.18.6
	sigs.k8s.io/controller-runtime v0.6.1
)

replace juniper.net/contrail => ../contrail
