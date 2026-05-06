package worker

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultRotateAge is the threshold past which uncompressed deploy logs
// get gzipped. 30 days matches the §8e plan; tunable via RotateDeployLogs.
const DefaultRotateAge = 30 * 24 * time.Hour

// DefaultPurgeAge is the threshold past which gzipped deploy logs are
// deleted entirely. 1 year by default; tunable.
const DefaultPurgeAge = 365 * 24 * time.Hour

// RotateDeployLogs walks {dataDir}/logs/deployments recursively and
//
//   - gzips any *.log file whose mtime is older than rotateAge,
//     producing *.log.gz and removing the original
//   - deletes any *.log.gz file whose mtime is older than purgeAge
//
// Returns counts for observability + the first error encountered. Per-
// file failures are logged and continue (one bad file doesn't halt the
// sweep).
//
// If rotateAge or purgeAge is 0, the corresponding default is used.
// Pass time.Hour * (-1) on either to disable that side.
func RotateDeployLogs(
	ctx context.Context,
	log *slog.Logger,
	dataDir string,
	rotateAge, purgeAge time.Duration,
	now time.Time,
) (rotated, purged int, firstErr error) {
	if log == nil {
		log = slog.Default()
	}
	if rotateAge == 0 {
		rotateAge = DefaultRotateAge
	}
	if purgeAge == 0 {
		purgeAge = DefaultPurgeAge
	}
	root := filepath.Join(dataDir, "logs", "deployments")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return 0, 0, nil // never deployed
	}

	rotateBefore := now.Add(-rotateAge)
	purgeBefore := now.Add(-purgeAge)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			log.Warn("rotate: walk error", "path", path, "error", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			log.Warn("rotate: stat", "path", path, "error", err)
			return nil
		}

		switch {
		case strings.HasSuffix(path, ".log.gz"):
			if purgeAge > 0 && info.ModTime().Before(purgeBefore) {
				if err := os.Remove(path); err != nil {
					log.Warn("rotate: purge", "path", path, "error", err)
					if firstErr == nil {
						firstErr = err
					}
					return nil
				}
				purged++
			}

		case strings.HasSuffix(path, ".log"):
			if rotateAge > 0 && info.ModTime().Before(rotateBefore) {
				if err := gzipAndRemove(path); err != nil {
					log.Warn("rotate: gzip", "path", path, "error", err)
					if firstErr == nil {
						firstErr = err
					}
					return nil
				}
				rotated++
			}
		}
		return nil
	})
	if walkErr != nil && firstErr == nil {
		firstErr = walkErr
	}
	if rotated > 0 || purged > 0 {
		log.Info("deploy log rotation", "rotated", rotated, "purged", purged)
	}
	return rotated, purged, firstErr
}

// gzipAndRemove copies path to path+".gz" using gzip compression, then
// removes the original. Atomic at the level of "the .gz file appears
// fully populated before the .log file disappears".
func gzipAndRemove(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer in.Close()

	outPath := path + ".gz"
	tmpPath := outPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmpPath, err)
	}
	gz := gzip.NewWriter(out)

	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("compress %s: %w", path, err)
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("flush gzip: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s: %w", tmpPath, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove original %s: %w", path, err)
	}
	return nil
}
