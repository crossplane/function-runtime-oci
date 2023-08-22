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

// Package spark runs a Composition Function. It is designed to be run as root
// inside an unprivileged user namespace.
package spark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/uuid"
	runtime "github.com/opencontainers/runtime-spec/specs-go"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/crossplane/function-runtime-oci/cmd/function-runtime-oci/internal/config"
	"github.com/crossplane/function-runtime-oci/internal/oci/spec"
	"github.com/crossplane/function-runtime-oci/internal/oci/store"
	"github.com/crossplane/function-runtime-oci/internal/oci/store/overlay"
	"github.com/crossplane/function-runtime-oci/internal/oci/store/uncompressed"
	"github.com/crossplane/function-runtime-oci/internal/proto/v1beta1"
)

// Error strings.
const (
	errReadRequest      = "cannot read request from stdin"
	errUnmarshalRequest = "cannot unmarshal request data from stdin"
	errNewBundleStore   = "cannot create OCI runtime bundle store"
	errOpenTarBall      = "cannot open OCI image tarball"
	errBundleFn         = "cannot create OCI runtime bundle"
	errMkRuntimeRootdir = "cannot make OCI runtime cache"
	errCleanupBundle    = "cannot cleanup OCI runtime bundle"
	errMarshalResponse  = "cannot marshal response data to stdout"
	errWriteResponse    = "cannot write response data to stdout"
	errCPULimit         = "cannot limit container CPU"
	errMemoryLimit      = "cannot limit container memory"
	errHostNetwork      = "cannot configure container to run in host network namespace"
	errMarshalRequest   = "cannot marshal request data to stdout"
)

// The path within the cache dir that the OCI runtime should use for its
// '--root' cache.
const ociRuntimeRoot = "runtime"

// Command runs a containerized Composition Function.
type Command struct {
	CacheDir               string `short:"c" help:"Directory used for caching function images and containers." default:"/function-runtime-oci-cache"`
	Runtime                string `help:"OCI runtime binary to invoke." default:"crun"`
	MaxStdioBytes          int64  `help:"Maximum size of stdout and stderr for functions." default:"0"`
	config.ResourcesConfig `embed:"" prefix:"resources"`
}

// Run a Composition Function inside an unprivileged user namespace. Reads a
// protocol buffer serialized RunFunctionRequest from stdin, and writes a
// protocol buffer serialized RunFunctionResponse to stdout.
func (c *Command) Run(args *config.Args) error { //nolint:gocyclo // TODO(negz): Refactor some of this out into functions, add tests.
	pb, err := io.ReadAll(os.Stdin)
	if err != nil {
		return errors.Wrap(err, errReadRequest)
	}

	req := &v1beta1.RunFunctionRequest{}
	if err := json.Unmarshal(pb, req); err != nil {
		return errors.Wrap(err, errUnmarshalRequest)
	}

	runID := uuid.NewString()

	// We prefer to use an overlayfs bundler where possible. It roughly doubles
	// the disk space per image because it caches layers as overlay compatible
	// directories in addition to the CachingImagePuller's cache of uncompressed
	// layer tarballs. The advantage is faster start times for containers with
	// cached image, because it creates an overlay rootfs. The uncompressed
	// bundler on the other hand must untar all of a containers layers to create
	// a new rootfs each time it runs a container.
	var s store.Bundler = uncompressed.NewBundler(c.CacheDir)
	if overlay.Supported(c.CacheDir) {
		s, err = overlay.NewCachingBundler(c.CacheDir)
	}
	if err != nil {
		return errors.Wrap(err, errNewBundleStore)
	}

	// We cache the image to the filesystem. Layers are cached as uncompressed
	// tarballs. This allows them to be extracted quickly when using the
	// uncompressed.Bundler, which extracts a new root filesystem for every
	// container run.
	img, err := tarball.ImageFromPath(args.ImageTarBall, nil)
	if err != nil {
		return errors.Wrap(err, errOpenTarBall)
	}

	ctx := context.Background()
	// Create an OCI runtime bundle for this container run.
	b, err := s.Bundle(ctx, img, runID, FromResourcesConfig(&c.ResourcesConfig))
	if err != nil {
		return errors.Wrap(err, errBundleFn)
	}

	root := filepath.Join(c.CacheDir, ociRuntimeRoot)
	if err := os.MkdirAll(root, 0700); err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, errMkRuntimeRootdir)
	}

	// TODO(negz): Consider using the OCI runtime's lifecycle management commands
	// (i.e create, start, and delete) rather than run. This would allow spark
	// to return without sitting in-between function-runtime-oci and crun. It's also generally
	// recommended; 'run' is more for testing. In practice though run seems to
	// work just fine for our use case.

	//nolint:gosec // Executing with user-supplied input is intentional.
	cmd := exec.CommandContext(ctx, c.Runtime, "--root="+root, "run", "--bundle="+b.Path(), runID)
	reqJSON, err := protojson.Marshal(req)
	if err != nil {
		return errors.Wrap(err, errMarshalRequest)
	}
	cmd.Stdin = bytes.NewReader(reqJSON)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = b.Cleanup()

		return errors.Wrap(err, "cannot get stdout pipe")
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, "cannot get stderr pipe")
	}

	if err := cmd.Start(); err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, "cannot start command")
	}

	stdout, err := io.ReadAll(limitReaderIfNonZero(stdoutPipe, c.MaxStdioBytes))
	if err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, "cannot read stdout")
	}
	stderr, err := io.ReadAll(limitReaderIfNonZero(stderrPipe, c.MaxStdioBytes))
	if err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, "cannot read stderr")
	}

	if err := cmd.Wait(); err != nil {
		msg := "while waiting for command"
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg = fmt.Sprintf("%s: %s", msg, string(stderr))
		}
		_ = b.Cleanup()
		return errors.Wrap(err, msg)
	}

	if err := b.Cleanup(); err != nil {
		return errors.Wrap(err, errCleanupBundle)
	}

	_, err = os.Stdout.Write(stdout)
	return errors.Wrap(err, errWriteResponse)
}

func limitReaderIfNonZero(r io.Reader, limit int64) io.Reader {
	if limit == 0 {
		return r
	}
	return io.LimitReader(r, limit)
}

// FromResourcesConfig extends a runtime spec with configuration derived from
// the supplied RunFunctionConfig.
func FromResourcesConfig(cfg *config.ResourcesConfig) spec.Option {
	return func(s *runtime.Spec) error {
		if cfg == nil {
			return nil
		}
		if l := cfg.CPULimit; l != "" {
			if err := spec.WithCPULimit(l)(s); err != nil {
				return errors.Wrap(err, errCPULimit)
			}
		}

		if l := cfg.MemoryLimit; l != "" {
			if err := spec.WithMemoryLimit(l)(s); err != nil {
				return errors.Wrap(err, errMemoryLimit)
			}
		}

		if cfg.NetworkPolicy == config.NetworkPolicyRunner {
			if err := spec.WithHostNetwork()(s); err != nil {
				return errors.Wrap(err, errHostNetwork)
			}
		}

		return nil
	}
}
