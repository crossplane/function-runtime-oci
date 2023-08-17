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
	Registry string `short:"r" help:"Default registry used to fetch containers when not specified in tag." default:"${default_registry}" env:"REGISTRY"`
}
