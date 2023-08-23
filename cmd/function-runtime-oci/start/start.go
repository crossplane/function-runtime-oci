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
	"compress/gzip"
	"io"
	"os"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/logging"

	"github.com/crossplane/function-runtime-oci/cmd/function-runtime-oci/internal/config"
	"github.com/crossplane/function-runtime-oci/internal/container"
)

// Error strings
const (
	errListenAndServe = "cannot listen for and serve gRPC API"
)

// Command starts a gRPC API to run Composition Functions.
type Command struct {
	CacheDir   string `short:"c" help:"Directory used for caching function images and containers." default:"/function-runtime-oci-cache"`
	MapRootUID int    `help:"UID that will map to 0 in the function's user namespace. The following 65336 UIDs must be available. Ignored if function-runtime-oci does not have CAP_SETUID and CAP_SETGID." default:"100000"`
	MapRootGID int    `help:"GID that will map to 0 in the function's user namespace. The following 65336 GIDs must be available. Ignored if function-runtime-oci does not have CAP_SETUID and CAP_SETGID." default:"100000"`
	Network    string `help:"Network on which to listen for gRPC connections." default:"tcp"`
	Address    string `help:"Address at which to listen for gRPC connections." default:"0.0.0.0:1234"`
}

// Run a Composition Function gRPC API.
func (c *Command) Run(args *config.Args, log logging.Logger) error {
	// If we don't have CAP_SETUID or CAP_SETGID, we'll only be able to map our
	// own UID and GID to root inside the user namespace.
	rootUID := os.Getuid()
	rootGID := os.Getgid()
	setuid := container.HasCapSetUID() && container.HasCapSetGID() // We're using 'setuid' as shorthand for both here.
	if setuid {
		log.Debug("CAP_SETUID and CAP_SETGID are available")
		rootUID = c.MapRootUID
		rootGID = c.MapRootGID
	}
	log.Debug("root UID and GID in function's user namespace", "uid", rootUID, "gid", rootGID)

	compressedTarball, err := os.Open(args.ImageTarBall)
	if err != nil {
		return errors.Wrap(err, "cannot open image tarball")
	}
	defer func() {
		_ = compressedTarball.Close()
	}()
	dst, err := os.Create(filepath.Join(c.CacheDir, args.ImageTarBall))
	if err != nil {
		return errors.Wrap(err, "cannot open destination file for tarball")
	}
	src, err := gzip.NewReader(compressedTarball)
	if err != nil {
		return errors.Wrap(err, "cannot create gzip reader")
	}
	_, err = copyChunks(dst, src, 1024*1024)
	if err != nil {
		return errors.Wrap(err, "cannot decompress image tarball")
	}

	log.Debug("image tarball copied to cache", "src", args.ImageTarBall, "path", dst.Name())

	// TODO(negz): Expose a healthz endpoint and otel metrics.
	f := container.NewRunner(
		container.SetUID(setuid),
		container.MapToRoot(rootUID, rootGID),
		container.WithLogger(log),
		container.WithImageTarBall(dst.Name()))
	return errors.Wrap(f.ListenAndServe(c.Network, c.Address), errListenAndServe)
}

// copyChunks pleases gosec per https://github.com/securego/gosec/pull/433.
// Like Copy it reads from src until EOF, it does not treat an EOF from Read as
// an error to be reported.
//
// NOTE(negz): This rule confused me at first because io.Copy appears to use a
// buffer, but in fact it bypasses it if src/dst is an io.WriterTo/ReaderFrom.
func copyChunks(dst io.Writer, src io.Reader, chunkSize int64) (int64, error) {
	var written int64
	for {
		w, err := io.CopyN(dst, src, chunkSize)
		written += w
		if errors.Is(err, io.EOF) {
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}
