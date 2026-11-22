package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
	"github.com/spf13/cobra"
)

func newDeployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Enqueue and follow a deployment",
		Long: `Triggers a deployment for a project. By default, follows the
deployment output until it finishes (like 'git push').

Use --no-follow to enqueue and exit immediately.

--image deploys a prebuilt image instead of building one. The reference
must be pinned by digest. A bare reference is the "default" image; use
name=ref for other cobalt.json images. Cobalt still checks out --commit to
read cobalt.json, and refuses an image whose
org.opencontainers.image.revision label names another commit.

Examples:
  cobalt deploy --project api
  cobalt deploy --project server --commit 1a2b3c4 \
    --image ghcr.io/heyblueteam/blue-server@sha256:<digest>`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			pc, err := newProjectClient(cmd)
			if err != nil {
				return err
			}
			commit, _ := cmd.Flags().GetString("commit")
			noCache, _ := cmd.Flags().GetBool("no-cache")
			noFollow, _ := cmd.Flags().GetBool("no-follow")
			force, _ := cmd.Flags().GetBool("force")
			filePath, _ := cmd.Flags().GetString("file")
			imageFlags, _ := cmd.Flags().GetStringArray("image")
			images, err := parseImageFlags(imageFlags)
			if err != nil {
				return err
			}

			req := cobaltapi.DeploymentCreateRequest{
				Commit:  commit,
				NoCache: noCache,
				Force:   force,
				Images:  images,
			}

			if filePath != "" {
				data, err := os.ReadFile(filePath)
				if err != nil {
					return fmt.Errorf("read file %q: %w", filePath, err)
				}
				if !json.Valid(data) {
					return fmt.Errorf("file %q is not valid JSON", filePath)
				}
				req.CobaltfileOverride = string(data)
			}

			resp, err := pc.CreateDeployment(cmd.Context(), pc.WrapProject(), req)
			if err != nil {
				return err
			}
			d := &resp.Deployment

			if resp.CancelledInflightId != 0 && !output.IsJSON() {
				output.PrintLines(fmt.Sprintf("Cancelled in-flight deployment %d", resp.CancelledInflightId))
			}

			if noFollow {
				if output.IsJSON() {
					output.PrintJSON(resp)
				} else {
					output.PrintLines(client.FormatDeployment(d))
				}
				return nil
			}

			status := string(d.Status)
			if err := output.FollowDeployOutput(cmd.Context(), pc.Client, d.ID, 0, output.Stdout); err != nil {
				if !cobaltapi.IsContextCanceled(err) {
					return err
				}
			}

			final, ferr := pc.GetDeployment(cmd.Context(), d.ID)
			if ferr == nil {
				status = string(final.Status)
			}

			colored := output.ColorStatus(status)
			output.PrintLines("")
			output.PrintLines("Deployment " + client.FormatDeployment(d) + " " + colored)

			if status == string(cobaltapi.StateFailed) {
				return fmt.Errorf("deployment failed")
			}
			return nil
		}),
	}
	cmd.Flags().String("project", "", "project name")
	cmd.Flags().String("commit", "", "git commit to deploy (defaults to latest on branch)")
	cmd.Flags().Bool("no-cache", false, "disable docker build cache")
	cmd.Flags().Bool("no-follow", false, "enqueue and exit without waiting")
	cmd.Flags().Bool("force", false, "cancel any in-flight fetch/build for this project before enqueuing (rejected if the in-flight deploy is already in cutover)")
	cmd.Flags().String("file", "", "path to a cobalt.json override file")
	cmd.Flags().StringArray("image", nil, "deploy a prebuilt image: [name=]ref@sha256:<digest> (repeatable; bare ref is the default image)")
	return cmd
}

// parseImageFlags turns repeated --image values into the request map. A
// value without "name=" is the "default" image. A "=" inside a reference
// is never valid, so the first "=" always separates the name.
func parseImageFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for _, v := range values {
		name, ref, hasName := strings.Cut(v, "=")
		if !hasName {
			name, ref = "default", v
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("--image: image %q given twice", name)
		}
		if err := validator.ValidateImageName(name); err != nil {
			return nil, fmt.Errorf("--image: %w", err)
		}
		if err := validator.ValidateImageDigestRef(ref); err != nil {
			return nil, fmt.Errorf("--image: %w", err)
		}
		out[name] = ref
	}
	return out, nil
}
