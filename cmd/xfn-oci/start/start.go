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

// Package start implements the reference Composition Function runner.
// It exposes a gRPC API that may be used to run Composition Functions.
package start

import (
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/logging"

	"github.com/upbound/xfn-oci/internal/container"
)

// Error strings
const (
	errListen = "cannot listen for gRPC connections"
	errServe  = "cannot serve gRPC API"
)

// Command starts a gRPC API to run Composition Functions.
type Command struct {
	CacheDir   string `short:"c" help:"Directory used for caching function images and containers." default:"/xfn"`
	MapRootUID int    `help:"UID that will map to 0 in the function's user namespace. The following 65336 UIDs must be available. Ignored if xfn does not have CAP_SETUID and CAP_SETGID." default:"100000"`
	MapRootGID int    `help:"GID that will map to 0 in the function's user namespace. The following 65336 GIDs must be available. Ignored if xfn does not have CAP_SETUID and CAP_SETGID." default:"100000"`
	Network    string `help:"Network on which to listen for gRPC connections." default:"tcp"`
	Address    string `help:"Address at which to listen for gRPC connections." default:"0.0.0.0:1234"`
}

// Run a Composition Function gRPC API.
func (c *Command) Run(registry, defaultImage string, log logging.Logger) error {
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
	fv1beta1 := container.NewContainerRunner(
		container.SetUID(setuid),
		container.MapToRoot(rootUID, rootGID),
		container.WithCacheDir(filepath.Clean(c.CacheDir)),
		container.WithLogger(log),
		container.WithRegistry(registry),
		container.WithDefaultImage(defaultImage),
	)

	log.Debug("Listening", "network", c.Network, "address", c.Address)
	lis, err := net.Listen(c.Network, c.Address)
	if err != nil {
		return errors.Wrap(err, errListen)
	}

	// TODO(negz): Limit concurrent function runs?
	srv := grpc.NewServer()
	if err := fv1beta1.Register(srv); err != nil {
		return errors.Wrap(err, "cannot register v1beta1")
	}

	return errors.Wrap(srv.Serve(lis), errServe)
}
