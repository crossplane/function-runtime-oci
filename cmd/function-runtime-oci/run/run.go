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

// Package run implements a convenience CLI to run and test Composition Functions.
package run

import (
	"context"
	"os"

	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/pkg/logging"

	"github.com/crossplane/function-runtime-oci/cmd/function-runtime-oci/internal/config"
	"github.com/crossplane/function-runtime-oci/internal/container"
	"github.com/crossplane/function-runtime-oci/internal/proto/v1beta1"
)

// Error strings
const (
	errReadRunFunctionRequest = "cannot read RunFunctionRequest"
	errRunFunction            = "cannot run function"
	errWriteFIO               = "cannot write fio"
	errMarshalResponse        = "cannot marshal response"
)

// Command runs a Composition function.
type Command struct {
	MapRootUID int `help:"UID that will map to 0 in the function's user namespace. The following 65336 UIDs must be available. Ignored if function-runtime-oci does not have CAP_SETUID and CAP_SETGID." default:"100000"`
	MapRootGID int `help:"GID that will map to 0 in the function's user namespace. The following 65336 GIDs must be available. Ignored if function-runtime-oci does not have CAP_SETUID and CAP_SETGID." default:"100000"`

	// TODO(negz): filecontent appears to take multiple args when it does not.
	// Bump kong once https://github.com/alecthomas/kong/issues/346 is fixed.

	ImageTarBall       string `arg:"" help:"OCI image to run."`
	RunFunctionRequest []byte `arg:"" help:"YAML encoded RunFunctionRequest to pass to the function." type:"filecontent"`
}

// Run a Composition container function.
func (c *Command) Run(args *config.Args, log logging.Logger) error {
	// If we don't have CAP_SETUID or CAP_SETGID, we'll only be able to map our
	// own UID and GID to root inside the user namespace.
	rootUID := os.Getuid()
	rootGID := os.Getgid()
	setuid := container.HasCapSetUID() && container.HasCapSetGID() // We're using 'setuid' as shorthand for both here.
	if setuid {
		rootUID = c.MapRootUID
		rootGID = c.MapRootGID
	}
	// TODO(negz): Expose a healthz endpoint and otel metrics.
	runner := container.NewRunner(
		container.SetUID(setuid),
		container.MapToRoot(rootUID, rootGID),
		container.WithLogger(log),
		container.WithImageTarBall(args.ImageTarBall))

	var req v1beta1.RunFunctionRequest
	err := yaml.Unmarshal(c.RunFunctionRequest, &req)
	if err != nil {
		return errors.Wrap(err, errReadRunFunctionRequest)
	}
	resp, err := runner.RunFunction(context.Background(), &req)
	if err != nil {
		return errors.Wrap(err, errRunFunction)
	}

	b, err := yaml.Marshal(resp)
	if err != nil {
		return errors.Wrap(err, errMarshalResponse)
	}
	_, err = os.Stdout.Write(b)
	return errors.Wrap(err, errWriteFIO)
}
