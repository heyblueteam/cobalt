package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

type cacheDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type cacheManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Manifests     []cacheDescriptor `json:"manifests"`
	Config        *cacheDescriptor  `json:"config"`
	Layers        []cacheDescriptor `json:"layers"`
	Subject       *cacheDescriptor  `json:"subject"`
}

// pruneLocalBuildCache removes historical exports that index.json no longer
// references. BuildKit's local exporter replaces the index but leaves old blobs
// behind, including intermediate compilation layers exported with mode=max.
// The deployment dispatcher serializes builds for each project/cache directory.
// Call only after buildx exits successfully, when export has finished.
func pruneLocalBuildCache(ctx context.Context, dir string) (removed int, freed int64, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = root.Close() }()
	index, err := root.ReadFile("index.json")
	if err != nil {
		return 0, 0, err
	}
	var manifest cacheManifest
	if err := json.Unmarshal(index, &manifest); err != nil {
		return 0, 0, err
	}
	if manifest.SchemaVersion != 2 || !cacheIndexType(manifest.MediaType) || len(manifest.Manifests) == 0 {
		return 0, 0, fmt.Errorf("invalid build cache index")
	}

	// Validate the complete graph before deleting anything. Retain every tag,
	// nested index, manifest, config and layer, including shared dependencies.
	keep := make(map[string]cacheDescriptor)
	var visit func(cacheDescriptor) error
	visit = func(d cacheDescriptor) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		hexDigest, ok := strings.CutPrefix(d.Digest, "sha256:")
		if !ok || !cacheBlobName(hexDigest) {
			return fmt.Errorf("unsupported cache digest %q", d.Digest)
		}
		path := "blobs/sha256/" + hexDigest
		if previous, ok := keep[path]; ok {
			if previous != d {
				return fmt.Errorf("conflicting cache descriptors for %s", d.Digest)
			}
			return nil
		}
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != d.Size {
			return fmt.Errorf("invalid cache blob %s", d.Digest)
		}
		keep[path] = d
		switch d.MediaType {
		case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
			"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json":
			data, err := root.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != hexDigest {
				return fmt.Errorf("cache manifest digest mismatch: %s", d.Digest)
			}
			var child cacheManifest
			if err := json.Unmarshal(data, &child); err != nil {
				return err
			}
			if child.SchemaVersion != 2 || child.MediaType != d.MediaType {
				return fmt.Errorf("invalid cache manifest %s", d.Digest)
			}
			if cacheIndexType(child.MediaType) {
				if len(child.Manifests) == 0 {
					return fmt.Errorf("empty cache index %s", d.Digest)
				}
			} else if child.Config == nil || len(child.Layers) == 0 {
				return fmt.Errorf("incomplete cache manifest %s", d.Digest)
			}
			for _, ref := range append(child.Manifests, child.Layers...) {
				if err := visit(ref); err != nil {
					return err
				}
			}
			if child.Config != nil {
				if err := visit(*child.Config); err != nil {
					return err
				}
			}
			if child.Subject != nil {
				return visit(*child.Subject)
			}
		case "application/vnd.buildkit.cacheconfig.v0",
			"application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json",
			"application/vnd.oci.image.layer.v1.tar", "application/vnd.oci.image.layer.v1.tar+gzip", "application/vnd.oci.image.layer.v1.tar+zstd",
			"application/vnd.docker.image.rootfs.diff.tar", "application/vnd.docker.image.rootfs.diff.tar.gzip":
			// Leaf blobs need no parsing or decompression.
		default:
			return fmt.Errorf("unsupported cache media type %q", d.MediaType)
		}
		return nil
	}
	for _, d := range manifest.Manifests {
		if err := visit(d); err != nil {
			return 0, 0, err
		}
	}
	if manifest.Subject != nil {
		if err := visit(*manifest.Subject); err != nil {
			return 0, 0, err
		}
	}
	entries, err := fs.ReadDir(root.FS(), "blobs/sha256")
	if err != nil {
		return 0, 0, err
	}
	current, err := root.ReadFile("index.json")
	if err != nil {
		return 0, 0, err
	}
	if !bytes.Equal(current, index) {
		return 0, 0, fmt.Errorf("build cache index changed during cleanup")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, freed, err
		}
		path := "blobs/sha256/" + entry.Name()
		if _, ok := keep[path]; ok {
			continue
		}
		if !cacheBlobName(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return removed, freed, err
		}
		if err := root.Remove(path); err != nil {
			return removed, freed, err
		}
		removed++
		freed += info.Size()
	}
	return removed, freed, nil
}

func cacheIndexType(mediaType string) bool {
	return mediaType == "application/vnd.oci.image.index.v1+json" || mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}

func cacheBlobName(name string) bool {
	if len(name) != sha256.Size*2 {
		return false
	}
	for _, c := range name {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}
