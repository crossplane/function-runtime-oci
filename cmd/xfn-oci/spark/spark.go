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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/uuid"
	runtime "github.com/opencontainers/runtime-spec/specs-go"
	proto2 "google.golang.org/protobuf/proto"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	"github.com/crossplane/crossplane/apis/apiextensions/fn/proto/v1beta1"

	v1beta12 "github.com/upbound/xfn-oci/internal/container"
	"github.com/upbound/xfn-oci/internal/container/proto"
	"github.com/upbound/xfn-oci/internal/oci"
	"github.com/upbound/xfn-oci/internal/oci/spec"
	"github.com/upbound/xfn-oci/internal/oci/store"
	"github.com/upbound/xfn-oci/internal/oci/store/overlay"
	"github.com/upbound/xfn-oci/internal/oci/store/uncompressed"
)

// Error strings.
const (
	errReadRequest      = "cannot read request from stdin"
	errUnmarshalRequest = "cannot unmarshal request data from stdin"
	errNewBundleStore   = "cannot create OCI runtime bundle store"
	errNewDigestStore   = "cannot create OCI image digest store"
	errParseRef         = "cannot parse OCI image reference"
	errPull             = "cannot pull OCI image"
	errBundleFn         = "cannot create OCI runtime bundle"
	errMkRuntimeRootdir = "cannot make OCI runtime cache"
	errRuntime          = "OCI runtime error"
	errCleanupBundle    = "cannot cleanup OCI runtime bundle"
	errMarshalResponse  = "cannot marshal response data to stdout"
	errWriteResponse    = "cannot write response data to stdout"
	errCPULimit         = "cannot limit container CPU"
	errMemoryLimit      = "cannot limit container memory"
	errHostNetwork      = "cannot configure container to run in host network namespace"
)

// The path within the cache dir that the OCI runtime should use for its
// '--root' cache.
const ociRuntimeRoot = "runtime"

// The time after which the OCI runtime will be killed if none is specified in
// the RunFunctionRequest.
const defaultTimeout = 25 * time.Second

// Command runs a containerized Composition Function.
type Command struct {
	CacheDir      string `short:"c" help:"Directory used for caching function images and containers." default:"/xfn"`
	Runtime       string `help:"OCI runtime binary to invoke." default:"crun"`
	MaxStdioBytes int64  `help:"Maximum size of stdout and stderr for functions." default:"0"`
	CABundlePath  string `help:"Additional CA bundle to use when fetching function images from registry." env:"CA_BUNDLE_PATH"`
}

// Run a Composition Function inside an unprivileged user namespace. Reads a
// protocol buffer serialized RunFunctionRequest from stdin, and writes a
// protocol buffer serialized RunFunctionResponse to stdout.
func (c *Command) Run(registry, defaultImage string) error { //nolint:gocyclo // TODO(negz): Refactor some of this out into functions, add tests.
	pb, err := io.ReadAll(os.Stdin)
	if err != nil {
		return errors.Wrap(err, errReadRequest)
	}

	req := &v1beta1.RunFunctionRequest{}
	if err := proto2.Unmarshal(pb, req); err != nil {
		return errors.Wrap(err, errUnmarshalRequest)
	}

	input := req.GetInput()
	if input == nil {
		return errors.New("no input provided")
	}

	confJSON, err := input.MarshalJSON()
	if err != nil {
		return errors.Wrap(err, "failed to marshal input to JSON")
	}
	var conf v1beta12.RunFunctionRequestConfig
	if err := json.Unmarshal(confJSON, &conf); err != nil {
		return errors.Wrapf(err, "failed to unmarshal input as config %s", string(confJSON))
	}

	t := conf.Spec.GetRunFunctionConfig().GetTimeout().AsDuration()
	if t == 0 {
		t = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()

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

	// This store maps OCI references to their last known digests. We use it to
	// resolve references when the imagePullPolicy is Never or IfNotPresent.
	h, err := store.NewDigest(c.CacheDir)
	if err != nil {
		return errors.Wrap(err, errNewDigestStore)
	}

	img := conf.Spec.GetImage()
	if img == "" {
		img = defaultImage
	}

	r, err := name.ParseReference(img, name.WithDefaultRegistry(registry))
	if err != nil {
		return errors.Wrap(err, errParseRef)
	}

	opts := []oci.ImageClientOption{FromImagePullConfig(conf.Spec.GetImagePullConfig())}
	if c.CABundlePath != "" {
		rootCA, err := oci.ParseCertificatesFromPath(c.CABundlePath)
		if err != nil {
			return errors.Wrap(err, "Cannot parse CA bundle")
		}
		opts = append(opts, oci.WithCustomCA(rootCA))
	}
	// We cache every image we pull to the filesystem. Layers are cached as
	// uncompressed tarballs. This allows them to be extracted quickly when
	// using the uncompressed.Bundler, which extracts a new root filesystem for
	// every container run.
	p := oci.NewCachingPuller(h, store.NewImage(c.CacheDir), &oci.RemoteClient{})
	image, err := p.Image(ctx, r, opts...)
	if err != nil {
		return errors.Wrap(err, errPull)
	}

	// Create an OCI runtime bundle for this container run.
	b, err := s.Bundle(ctx, image, runID, FromRunFunctionConfig(conf.Spec.GetRunFunctionConfig()))
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
	// to return without sitting in-between xfn and crun. It's also generally
	// recommended; 'run' is more for testing. In practice though run seems to
	// work just fine for our use case.

	//nolint:gosec // Executing with user-supplied input is intentional.
	cmd := exec.CommandContext(ctx, c.Runtime, "--root="+root, "run", "--bundle="+b.Path(), runID)
	reqJSON, err := json.Marshal(req)
	if err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, "failed to marshal request to JSON")
	}
	cmd.Stdin = bytes.NewReader(reqJSON)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, errRuntime)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, errRuntime)
	}

	if err := cmd.Start(); err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, errRuntime)
	}

	stdout, err := io.ReadAll(limitReaderIfNonZero(stdoutPipe, c.MaxStdioBytes))
	if err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, errRuntime)
	}
	stderr, err := io.ReadAll(limitReaderIfNonZero(stderrPipe, c.MaxStdioBytes))
	if err != nil {
		_ = b.Cleanup()
		return errors.Wrap(err, errRuntime)
	}

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr
		}
		_ = b.Cleanup()
		return errors.Wrap(err, errRuntime)
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

// FromImagePullConfig configures an image client with options derived from the
// supplied ImagePullConfig.
func FromImagePullConfig(cfg *proto.ImagePullConfig) oci.ImageClientOption {
	return func(o *oci.ImageClientOptions) {
		switch cfg.GetPullPolicy() {
		case proto.ImagePullPolicy_IMAGE_PULL_POLICY_ALWAYS:
			oci.WithPullPolicy(oci.ImagePullPolicyAlways)(o)
		case proto.ImagePullPolicy_IMAGE_PULL_POLICY_NEVER:
			oci.WithPullPolicy(oci.ImagePullPolicyNever)(o)
		case proto.ImagePullPolicy_IMAGE_PULL_POLICY_IF_NOT_PRESENT, proto.ImagePullPolicy_IMAGE_PULL_POLICY_UNSPECIFIED:
			oci.WithPullPolicy(oci.ImagePullPolicyIfNotPresent)(o)
		}
		if a := cfg.GetAuth(); a != nil {
			oci.WithPullAuth(&oci.ImagePullAuth{
				Username:      a.GetUsername(),
				Password:      a.GetPassword(),
				Auth:          a.GetAuth(),
				IdentityToken: a.GetIdentityToken(),
				RegistryToken: a.GetRegistryToken(),
			})(o)
		}
	}
}

// FromRunFunctionConfig extends a runtime spec with configuration derived from
// the supplied RunFunctionConfig.
func FromRunFunctionConfig(cfg *proto.RunFunctionConfig) spec.Option {
	return func(s *runtime.Spec) error {
		if l := cfg.GetResources().GetLimits().GetCpu(); l != "" {
			if err := spec.WithCPULimit(l)(s); err != nil {
				return errors.Wrap(err, errCPULimit)
			}
		}

		if l := cfg.GetResources().GetLimits().GetMemory(); l != "" {
			if err := spec.WithMemoryLimit(l)(s); err != nil {
				return errors.Wrap(err, errMemoryLimit)
			}
		}

		if cfg.GetNetwork().GetPolicy() == proto.NetworkPolicy_NETWORK_POLICY_RUNNER {
			if err := spec.WithHostNetwork()(s); err != nil {
				return errors.Wrap(err, errHostNetwork)
			}
		}

		return nil
	}
}
