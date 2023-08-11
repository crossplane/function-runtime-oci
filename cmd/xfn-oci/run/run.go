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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	xpFn "github.com/crossplane/crossplane/apis/apiextensions/fn/proto/v1beta1"

	"github.com/upbound/xfn-oci/internal/container"
	"github.com/upbound/xfn-oci/internal/container/proto"
)

// Error strings
const (
	errWriteFIO        = "cannot write YAML to stdout"
	errRunFunction     = "cannot run function"
	errParseImage      = "cannot parse image reference"
	errResolveKeychain = "cannot resolve default registry authentication keychain"
	errAuthCfg         = "cannot get default registry authentication credentials"
	errParseInput      = "cannot parse input"
)

// Command runs a Composition function.
type Command struct {
	CacheDir        string        `short:"c" help:"Directory used for caching function images and containers." default:"/xfn"`
	Timeout         time.Duration `help:"Maximum time for which the function may run before being killed." default:"30s"`
	ImagePullPolicy string        `help:"Whether the image may be pulled from a remote registry." enum:"Always,Never,IfNotPresent" default:"IfNotPresent"`
	NetworkPolicy   string        `help:"Whether the function may access the network." enum:"Runner,Isolated" default:"Isolated"`
	MapRootUID      int           `help:"UID that will map to 0 in the function's user namespace. The following 65336 UIDs must be available. Ignored if xfn does not have CAP_SETUID and CAP_SETGID." default:"100000"`
	MapRootGID      int           `help:"GID that will map to 0 in the function's user namespace. The following 65336 GIDs must be available. Ignored if xfn does not have CAP_SETUID and CAP_SETGID." default:"100000"`

	// TODO(negz): filecontent appears to take multiple args when it does not.
	// Bump kong once https://github.com/alecthomas/kong/issues/346 is fixed.

	Input []byte `arg:"" help:"YAML encoded RunFunctionRequest to pass to the function." type:"filecontent"`
}

// Run a Composition container function.
func (c *Command) Run(registry, defaultImage string) error { //nolint:gocyclo // the complexity is in the switch statement
	// If we don't have CAP_SETUID or CAP_SETGID, we'll only be able to map our
	// own UID and GID to root inside the user namespace.
	rootUID := os.Getuid()
	rootGID := os.Getgid()
	setuid := container.HasCapSetUID() && container.HasCapSetGID() // We're using 'setuid' as shorthand for both here.
	if setuid {
		rootUID = c.MapRootUID
		rootGID = c.MapRootGID
	}

	ref, err := name.ParseReference(defaultImage, name.WithDefaultRegistry(registry))
	if err != nil {
		return errors.Wrap(err, errParseImage)
	}

	var req xpFn.RunFunctionRequest
	if err := json.Unmarshal(c.Input, &req); err != nil {
		fmt.Println(string(c.Input))
		return errors.Wrap(err, errParseInput)
	}
	var cfg *container.RunFunctionRequestConfig
	cfgStruct := req.GetInput()
	if cfgStruct != nil {
		cfg, err = container.ConfigFromRequest(&req)
		if err != nil {
			return err
		}
	}
	if cfg.Spec.ImagePullConfig == nil {
		cfg.Spec.ImagePullConfig = &proto.ImagePullConfig{}
	}
	if cfg.Spec.ImagePullConfig.Auth == nil {
		// We want to resolve authentication credentials here, using the caller's
		// environment rather than inside the user namespace that spark will create.
		// DefaultKeychain uses credentials from ~/.docker/config.json to pull
		// private images. Despite being 'the default' it must be explicitly
		// provided, or go-containerregistry will use anonymous authentication.
		auth, err := authn.DefaultKeychain.Resolve(ref.Context())
		if err != nil {
			return errors.Wrap(err, errResolveKeychain)
		}

		a, err := auth.Authorization()
		if err != nil {
			return errors.Wrap(err, errAuthCfg)
		}
		cfg.Spec.ImagePullConfig.Auth = &proto.ImagePullAuth{
			Username:      a.Username,
			Password:      a.Password,
			Auth:          a.Auth,
			IdentityToken: a.IdentityToken,
			RegistryToken: a.RegistryToken,
		}

	}
	f := container.NewContainerRunner(container.SetUID(setuid), container.MapToRoot(rootUID, rootGID), container.WithCacheDir(filepath.Clean(c.CacheDir)), container.WithRegistry(registry))
	rsp, err := f.RunFunction(context.Background(), &req)
	if err != nil {
		return errors.Wrap(err, errRunFunction)
	}
	_, err = os.Stdout.WriteString(rsp.String())

	return errors.Wrap(err, errWriteFIO)
}
