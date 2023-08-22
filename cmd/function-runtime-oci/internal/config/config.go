/*
Copyright 2022 The Crossplane Authors.

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

// Package config contains the global config for all commands
package config

// Args contains the default registry used to pull XFN containers.
type Args struct {
	ImageTarBall string `short:"i" help:"Image tarball to be used for the function." default:"" env:"IMAGE_TARBALL"`
}

// ResourcesConfig contains the resources configuration for the function.
type ResourcesConfig struct {
	MemoryLimit   string        `help:"Memory, in bytes. (500Gi = 500GiB = 500 * 1024 * 1024 * 1024). Specified in Kubernetes-style resource.Quantity form." default:""`
	CPULimit      string        `help:"CPU, in cores. (500m = .5 cores). Specified in Kubernetes-style resource.Quantity form." default:""`
	NetworkPolicy NetworkPolicy `help:"NetworkPolicy configures whether a container is isolated from the network." enum:"Runner,Isolated" default:"Isolated"`
}

// NetworkPolicy configures whether a container is isolated from the network.
type NetworkPolicy string

const (
	// NetworkPolicyRunner allows the container to access the same network as the function runner.
	NetworkPolicyRunner NetworkPolicy = "Runner"
	// NetworkPolicyIsolated runs the container without network access. The default.
	NetworkPolicyIsolated NetworkPolicy = "Isolated"
)
