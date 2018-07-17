package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/containers/image/directory"
	"github.com/containers/image/image"
	"github.com/containers/image/pkg/blobinfocache"
	"github.com/containers/image/types"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

type layersOptions struct {
	global *globalOptions
	image  *imageOptions
}

func layersCmd(global *globalOptions) cli.Command {
	imageFlags, imageOpts := imageFlags(global, "", "")
	opts := layersOptions{
		global: global,
		image:  imageOpts,
	}
	return cli.Command{
		Name:      "layers",
		Usage:     "Get layers of IMAGE-NAME",
		ArgsUsage: "IMAGE-NAME [LAYER...]",
		Hidden:    true,
		Action:    opts.run,
		Flags:     imageFlags,
	}
}

func (opts *layersOptions) run(c *cli.Context) (retErr error) {
	fmt.Fprintln(os.Stderr, `DEPRECATED: skopeo layers is deprecated in favor of skopeo copy`)
	if c.NArg() == 0 {
		return errors.New("Usage: layers imageReference [layer...]")
	}

	ctx, cancel := opts.global.commandTimeoutContext()
	defer cancel()

	sys, err := contextFromImageOptions(c, opts.image)
	if err != nil {
		return err
	}
	cache := blobinfocache.DefaultCache(sys)
	rawSource, err := parseImageSource(ctx, c, opts.image, c.Args()[0])
	if err != nil {
		return err
	}
	src, err := image.FromSource(ctx, sys, rawSource)
	if err != nil {
		if closeErr := rawSource.Close(); closeErr != nil {
			return errors.Wrapf(err, " (close error: %v)", closeErr)
		}

		return err
	}
	defer func() {
		if err := src.Close(); err != nil {
			retErr = errors.Wrapf(retErr, " (close error: %v)", err)
		}
	}()

	type blobDigest struct {
		digest   digest.Digest
		isConfig bool
	}
	var blobDigests []blobDigest
	for _, dString := range c.Args().Tail() {
		if !strings.HasPrefix(dString, "sha256:") {
			dString = "sha256:" + dString
		}
		d, err := digest.Parse(dString)
		if err != nil {
			return err
		}
		blobDigests = append(blobDigests, blobDigest{digest: d, isConfig: false})
	}

	if len(blobDigests) == 0 {
		layers := src.LayerInfos()
		seenLayers := map[digest.Digest]struct{}{}
		for _, info := range layers {
			if _, ok := seenLayers[info.Digest]; !ok {
				blobDigests = append(blobDigests, blobDigest{digest: info.Digest, isConfig: false})
				seenLayers[info.Digest] = struct{}{}
			}
		}
		configInfo := src.ConfigInfo()
		if configInfo.Digest != "" {
			blobDigests = append(blobDigests, blobDigest{digest: configInfo.Digest, isConfig: true})
		}
	}

	tmpDir, err := ioutil.TempDir(".", "layers-")
	if err != nil {
		return err
	}
	tmpDirRef, err := directory.NewReference(tmpDir)
	if err != nil {
		return err
	}
	dest, err := tmpDirRef.NewImageDestination(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err := dest.Close(); err != nil {
			retErr = errors.Wrapf(retErr, " (close error: %v)", err)
		}
	}()

	for _, bd := range blobDigests {
		r, blobSize, err := rawSource.GetBlob(ctx, types.BlobInfo{Digest: bd.digest, Size: -1}, cache)
		if err != nil {
			return err
		}
		if _, err := dest.PutBlob(ctx, r, types.BlobInfo{Digest: bd.digest, Size: blobSize}, cache, bd.isConfig); err != nil {
			if closeErr := r.Close(); closeErr != nil {
				return errors.Wrapf(err, " (close error: %v)", closeErr)
			}
			return err
		}
	}

	manifest, _, err := src.Manifest(ctx)
	if err != nil {
		return err
	}
	if err := dest.PutManifest(ctx, manifest); err != nil {
		return err
	}

	return dest.Commit(ctx)
}
