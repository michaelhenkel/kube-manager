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

// KubeManagerConfiguration defines all config options for the kubemanager
// controller with their default values. Its values are supplied by envconfig in
// main.go. See https://github.com/kelseyhightower/envconfig for more info.
type KubeManagerConfiguration struct {
	ClusterName                  string   `default:"cluster1" split_words:"true"`
	ClusterProject               string   `default:"project-kubemanager" split_words:"true"`
	ClusterVirtualNetwork        string   `default:"virtualnetwork-kubemanager" split_words:"true"`
	ManagedClusterConfigContexts []string `default:"" split_words:"true"`
}
