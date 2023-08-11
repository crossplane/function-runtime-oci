//go:build generate
// +build generate

/*
Copyright 2019 The Crossplane Authors.

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

//go:generate go install google.golang.org/protobuf/cmd/protoc-gen-go google.golang.org/grpc/cmd/protoc-gen-go-grpc
//go:generate go run github.com/bufbuild/buf/cmd/buf generate

package container

import (
	_ "github.com/bufbuild/buf/cmd/buf"               //nolint:typecheck
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc" //nolint:typecheck
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"  //nolint:typecheck
	_ "k8s.io/code-generator"                         //nolint:typecheck
)
