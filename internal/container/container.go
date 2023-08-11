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

package container

import (
	"encoding/json"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/logging"

	"github.com/crossplane/crossplane/apis/apiextensions/fn/proto/v1beta1"

	"github.com/upbound/xfn-oci/internal/container/proto"
)

const (
	errUnmarshalConfig     = "cannot unmarshal input to RunFunctionRequestConfig"
	errMarshalRequestInput = "cannot marshal Input to JSON"
	errLinuxOnly           = "containerized functions are only supported on Linux"
)

const defaultCacheDir = "/xfn"

// RunFunctionRequestConfig is a request to run a Composition Function
// packaged as an OCI image.
type RunFunctionRequestConfig struct {
	metav1.TypeMeta `json:",inline"`

	Spec proto.RunFunctionRequestConfigSpec `json:"spec,omitempty"`
}

// An Runner runs a Composition Function packaged as an OCI image by
// extracting it and running it as a 'rootless' container.
type Runner struct {
	v1beta1.UnimplementedFunctionRunnerServiceServer

	log logging.Logger

	rootUID      int
	rootGID      int
	setuid       bool // Specifically, CAP_SETUID and CAP_SETGID.
	cache        string
	registry     string
	defaultImage *string
}

// A RunnerOption configures a new Runner.
type RunnerOption func(*Runner)

// MapToRoot configures what UID and GID should map to root (UID/GID 0) in the
// user namespace in which the function will be run.
func MapToRoot(uid, gid int) RunnerOption {
	return func(r *Runner) {
		r.rootUID = uid
		r.rootGID = gid
	}
}

// SetUID indicates that the container runner should attempt operations that
// require CAP_SETUID and CAP_SETGID, for example creating a user namespace that
// maps arbitrary UIDs and GIDs to the parent namespace.
func SetUID(s bool) RunnerOption {
	return func(r *Runner) {
		r.setuid = s
	}
}

// WithDefaultImage specifies the default image that should be used to run
// functions if no image is specified in the request.
func WithDefaultImage(image string) RunnerOption {
	return func(r *Runner) {
		if image == "" {
			return
		}
		r.defaultImage = &image
	}
}

// WithCacheDir specifies the directory used for caching function images and
// containers.
func WithCacheDir(d string) RunnerOption {
	return func(r *Runner) {
		r.cache = d
	}
}

// WithRegistry specifies the default registry used to retrieve function images and
// containers.
func WithRegistry(dr string) RunnerOption {
	return func(r *Runner) {
		r.registry = dr
	}
}

// WithLogger configures which logger the container runner should use. Logging
// is disabled by default.
func WithLogger(l logging.Logger) RunnerOption {
	return func(cr *Runner) {
		cr.log = l
	}
}

// NewContainerRunner returns a new Runner that runs functions as rootless
// containers.
func NewContainerRunner(o ...RunnerOption) *Runner {
	r := &Runner{cache: defaultCacheDir, log: logging.NewNopLogger()}
	for _, fn := range o {
		fn(r)
	}

	return r
}

// Register the container runner with the supplied gRPC server.
func (r *Runner) Register(srv *grpc.Server) error {
	// TODO(negz): Limit concurrent function runs?
	v1beta1.RegisterFunctionRunnerServiceServer(srv, r)
	reflection.Register(srv)
	return nil
}

// ConfigFromRequest extracts a RunFunctionRequestConfig from the supplied
// request.
func ConfigFromRequest(req *v1beta1.RunFunctionRequest) (*RunFunctionRequestConfig, error) {
	var cfg RunFunctionRequestConfig
	input := req.GetInput()
	if input == nil {
		return &cfg, nil
	}
	inputJSON, err := input.MarshalJSON()
	if err != nil {
		return nil, errors.Wrap(err, errMarshalRequestInput)
	}

	if err := json.Unmarshal(inputJSON, &cfg); err != nil {
		return nil, errors.Wrap(err, errUnmarshalConfig)
	}
	return &cfg, nil
}
