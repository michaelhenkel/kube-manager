
# Image URL to use all building/pushing image targets
IMG ?= contrail-kube-manager-controller
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd:trivialVersions=true"

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

all: mcmanager

# Run tests
test: manifests-contrail generate fmt vet manifests
	go test ./... -coverprofile cover.out
	go tool cover -func=cover.out

# Build manager binary
manager: generate fmt vet
	go build -o bin/manager main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: generate fmt vet manifests
	go run ./main.go

# Install CRDs into a cluster
install: manifests
	kustomize build config/crd | kubectl apply -f -

# Uninstall CRDs from a cluster
uninstall: manifests
	kustomize build config/crd | kubectl delete -f -

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: manifests
	cd config/manager && kustomize edit set image controller=${IMG}
	kustomize build config/default | kubectl apply -f -

# Generate manifests e.g. CRD, RBAC etc.
manifests: controller-gen
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Run go fmt against code
fmt:
	go fmt ./...

# Run go vet against code
vet:
	go vet ./...

# Generate code
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

# Build the docker image
docker-build: manifests-contrail generate fmt vet manifests
	rsync --archive --verbose --checksum --delete --quiet --exclude='**/cover.out' '--exclude=**/hack' '--exclude=Makefile' ../contrail .contrail
	docker build . -t ${IMG}

# Configure resources required to run
# Note that this should be run on a system where vhost0 exists
.PHONY: config
is_virtualnetwork_up = (kubectl get --namespace=project-kubemanager virtualnetworks.core.contrail.juniper.net virtualnetwork-kubemanager --no-headers | grep -q "Success$$")
create_namespace = kubectl create namespace $(1) 2>&1 | grep -v 'AlreadyExists' || true
config:
	$(call create_namespace,project-kubemanager)
	kubectl apply -f config/samples/
	$(is_virtualnetwork_up) || (sleep 20 && $(is_virtualnetwork_up))

# Run the docker image
export KUBECONFIG ?= $(HOME)/.kube/config
config_contexts = $(shell if [ "$$(kubectx | wc -l)" == "1" ]; then kubectx -c; else kubectx -c >/var/tmp/current.ctx && kubectx >/var/tmp/all.ctx && comm -3 /var/tmp/current.ctx /var/tmp/all.ctx | cut -f2 | tr '\n' ',' | sed 's:,$$::'; fi)
docker-run:
	docker ps -a | grep -q ${IMG} || (make docker-build config && chmod o+r "$(KUBECONFIG)" && docker run --detach --name ${IMG} --network=host --volume=$(KUBECONFIG):/config --env KUBEMANAGER_MANAGED_CLUSTER_CONFIG_CONTEXTS="$(config_contexts)" --env KUBECONFIG=/config ${IMG}:latest --kubeconfig=/config)

# Stop the docker image
docker-stop:
	docker rm -f ${IMG} 2>/dev/null || true

# Push the docker image
docker-push:
	docker push ${IMG}

manifests-contrail:
	cd ../contrail && $(MAKE) manifests

# find or download controller-gen
# download controller-gen if necessary
controller-gen:
ifeq (, $(shell which controller-gen))
	@{ \
	set -e ;\
	CONTROLLER_GEN_TMP_DIR=$$(mktemp -d) ;\
	cd $$CONTROLLER_GEN_TMP_DIR ;\
	go mod init tmp ;\
	go get sigs.k8s.io/controller-tools/cmd/controller-gen@v0.2.5 ;\
	rm -rf $$CONTROLLER_GEN_TMP_DIR ;\
	}
CONTROLLER_GEN=$(GOBIN)/controller-gen
else
CONTROLLER_GEN=$(shell which controller-gen)
endif
