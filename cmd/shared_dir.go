// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hubsync"
	"github.com/GoogleCloudPlatform/scion/pkg/projectfs"
)

var sharedDirCmd = &cobra.Command{
	Use:     "shared-dir",
	Aliases: []string{"sd"},
	Short:   "Manage project shared directories",
	Long: `Shared directories provide filesystem-level state sharing between agents in a project.

Without --project, or with --project set to a local project directory, the
commands read and write that directory's settings file. With --project set
to a hub project id (or any other hub project reference that is not a local
directory), list, create, info and remove use the Hub API through the same
authenticated client as the other hub commands. The declaration is stored
on that project. Pass the project id from a workspace directory: a relative
slug is a local path when a directory of that name exists.`,
}

var sharedDirListCmd = &cobra.Command{
	Use:   "list",
	Short: "List shared directories for the current project",
	RunE: func(cmd *cobra.Command, args []string) error {
		if sharedDirUsesHub() {
			return runHubSharedDirList(cmd)
		}
		projectDir, err := config.GetResolvedProjectDir(projectPath)
		if err != nil {
			return fmt.Errorf("failed to resolve project: %w", err)
		}

		settings, _, err := config.LoadEffectiveSettings(projectDir)
		if err != nil {
			return fmt.Errorf("failed to load settings: %w", err)
		}

		if len(settings.SharedDirs) == 0 {
			if isJSONOutput() {
				return outputJSON([]interface{}{})
			}
			fmt.Println("No shared directories configured.")
			return nil
		}

		infos, err := config.GetSharedDirInfos(projectDir, settings.SharedDirs)
		if err != nil {
			return fmt.Errorf("failed to get shared dir info: %w", err)
		}

		if isJSONOutput() {
			return outputJSON(infos)
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tMODE\tMOUNT\tPROVISIONED")
		for _, info := range infos {
			mode := "read-write"
			if info.ReadOnly {
				mode = "read-only"
			}
			mount := fmt.Sprintf("/scion-volumes/%s", info.Name)
			if info.InWorkspace {
				mount = fmt.Sprintf("/workspace/.scion-volumes/%s", info.Name)
			}
			provisioned := "no"
			if info.Exists {
				provisioned = "yes"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", info.Name, mode, mount, provisioned)
		}
		return tw.Flush()
	},
}

var sharedDirCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new shared directory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if sharedDirUsesHub() {
			readOnly, _ := cmd.Flags().GetBool("read-only")
			inWorkspace, _ := cmd.Flags().GetBool("in-workspace")
			return runHubSharedDirCreate(cmd, name, readOnly, inWorkspace)
		}

		// Validate the name
		if err := api.ValidateSharedDirs([]api.SharedDir{{Name: name}}); err != nil {
			return err
		}

		projectDir, err := config.GetResolvedProjectDir(projectPath)
		if err != nil {
			return fmt.Errorf("failed to resolve project: %w", err)
		}

		sdReadOnly, _ := cmd.Flags().GetBool("read-only")
		sdInWorkspace, _ := cmd.Flags().GetBool("in-workspace")

		newDir := api.SharedDir{
			Name:        name,
			ReadOnly:    sdReadOnly,
			InWorkspace: sdInWorkspace,
		}

		// The duplicate check and the append run under the settings lock,
		// from the read to the write, so a concurrent change is not lost.
		errExists := fmt.Errorf("shared directory %q already exists", name)
		if err := config.LoadModifySaveVersionedSettings(projectDir, func(vs *config.VersionedSettings) error {
			for _, d := range vs.SharedDirs {
				if d.Name == name {
					return errExists
				}
			}
			vs.SharedDirs = append(vs.SharedDirs, newDir)
			return nil
		}); err != nil {
			if errors.Is(err, errExists) {
				return err
			}
			return fmt.Errorf("failed to save settings: %w", err)
		}

		// Create the host-side directory
		if err := config.EnsureSharedDirs(projectDir, []api.SharedDir{newDir}); err != nil {
			return fmt.Errorf("failed to provision shared directory: %w", err)
		}

		if isJSONOutput() {
			return outputJSON(ActionResult{
				Status:  "success",
				Command: "shared-dir create",
				Message: fmt.Sprintf("Shared directory '%s' created.", name),
				Details: map[string]interface{}{
					"name":         name,
					"read_only":    sdReadOnly,
					"in_workspace": sdInWorkspace,
				},
			})
		}

		fmt.Printf("Shared directory '%s' created.\n", name)
		return nil
	},
}

var sharedDirRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a shared directory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if sharedDirUsesHub() {
			return runHubSharedDirRemove(cmd, name)
		}

		projectDir, err := config.GetResolvedProjectDir(projectPath)
		if err != nil {
			return fmt.Errorf("failed to resolve project: %w", err)
		}

		vs, err := config.LoadSingleFileVersioned(projectDir)
		if err != nil {
			return fmt.Errorf("failed to load settings: %w", err)
		}

		found := false
		for _, d := range vs.SharedDirs {
			if d.Name == name {
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("shared directory %q not found", name)
		}

		if !autoConfirm {
			fmt.Printf("Remove shared directory '%s' and all its contents? [y/N] ", name)
			var response string
			_, _ = fmt.Scanln(&response)
			if response != "y" && response != "Y" {
				fmt.Println("Cancelled.")
				return nil
			}
		}

		// Re-read under the settings lock (the prompt above is not held
		// under it) and remove the entry from the current file, so a
		// concurrent change is not lost.
		errGone := fmt.Errorf("shared directory %q not found", name)
		if err := config.LoadModifySaveVersionedSettings(projectDir, func(vs *config.VersionedSettings) error {
			kept := vs.SharedDirs[:0:0]
			for _, d := range vs.SharedDirs {
				if d.Name != name {
					kept = append(kept, d)
				}
			}
			if len(kept) == len(vs.SharedDirs) {
				return errGone
			}
			vs.SharedDirs = kept
			return nil
		}); err != nil {
			if errors.Is(err, errGone) {
				return err
			}
			return fmt.Errorf("failed to save settings: %w", err)
		}

		if err := config.RemoveSharedDir(projectDir, name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove host directory: %v\n", err)
		}

		if isJSONOutput() {
			return outputJSON(ActionResult{
				Status:  "success",
				Command: "shared-dir remove",
				Message: fmt.Sprintf("Shared directory '%s' removed.", name),
			})
		}

		fmt.Printf("Shared directory '%s' removed.\n", name)
		return nil
	},
}

var sharedDirInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show details about a shared directory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if sharedDirUsesHub() {
			return runHubSharedDirInfo(cmd, name)
		}

		projectDir, err := config.GetResolvedProjectDir(projectPath)
		if err != nil {
			return fmt.Errorf("failed to resolve project: %w", err)
		}

		settings, _, err := config.LoadEffectiveSettings(projectDir)
		if err != nil {
			return fmt.Errorf("failed to load settings: %w", err)
		}

		var target *api.SharedDir
		for _, d := range settings.SharedDirs {
			if d.Name == name {
				d := d
				target = &d
				break
			}
		}
		if target == nil {
			return fmt.Errorf("shared directory %q not found", name)
		}

		infos, err := config.GetSharedDirInfos(projectDir, []api.SharedDir{*target})
		if err != nil {
			return fmt.Errorf("failed to get shared dir info: %w", err)
		}
		info := infos[0]

		if isJSONOutput() {
			return outputJSON(info)
		}

		fmt.Printf("Name:         %s\n", info.Name)
		if info.ReadOnly {
			fmt.Println("Mode:         read-only")
		} else {
			fmt.Println("Mode:         read-write")
		}
		if info.InWorkspace {
			fmt.Printf("Mount:        /workspace/.scion-volumes/%s\n", info.Name)
		} else {
			fmt.Printf("Mount:        /scion-volumes/%s\n", info.Name)
		}
		fmt.Printf("Host path:    %s\n", info.HostPath)
		if info.Exists {
			fmt.Println("Provisioned:  yes")
		} else {
			fmt.Println("Provisioned:  no")
		}

		return nil
	},
}

// sharedDirUsesHub reports whether --project names a hub project rather
// than a local directory. A project id is a hub reference. A slug is one
// only when no local directory of that name exists.
func sharedDirUsesHub() bool {
	return projectPath != "" && hubsync.IsHubProjectRef(projectPath)
}

// hubSharedDirView is one shared directory as the CLI prints it. Mount is
// the container path from the hub. LocalHostPath is the local-backend leaf
// this workstation broker bind-mounts; it is computed from the project id
// and slug, not returned by the API.
type hubSharedDirView struct {
	Name          string `json:"name"`
	ReadOnly      bool   `json:"read_only"`
	InWorkspace   bool   `json:"in_workspace"`
	Mount         string `json:"mount"`
	LocalHostPath string `json:"local_host_path"`
	Exists        bool   `json:"exists"`
	ProjectID     string `json:"project_id"`
}

func requireHubSharedDirProject() (*HubContext, error) {
	hubCtx, err := requireHub(projectPath)
	if err != nil {
		return nil, err
	}
	if hubCtx.ProjectID == "" {
		return nil, fmt.Errorf("hub did not resolve a project for %q", projectPath)
	}
	if id, err := uuid.Parse(projectPath); err == nil && hubCtx.ProjectID != id.String() {
		return nil, fmt.Errorf("hub resolved project %s, not %s", hubCtx.ProjectID, id.String())
	}
	return hubCtx, nil
}

func hubSharedDirViews(ctx context.Context, hubCtx *HubContext) ([]hubSharedDirView, error) {
	dirs, err := hubCtx.Client.Projects().ListSharedDirs(ctx, hubCtx.ProjectID)
	if err != nil {
		return nil, err
	}
	project, err := hubCtx.Client.Projects().Get(ctx, hubCtx.ProjectID)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home for the local shared-dir path: %w", err)
	}
	views := make([]hubSharedDirView, 0, len(dirs))
	for _, dir := range dirs {
		mount := dir.Target
		if mount == "" {
			mount = projectfs.SharedDirTarget(dir.Name, dir.InWorkspace, "")
		}
		host := config.SharedDirHostPath(home, project.Slug, project.ID, dir.Name)
		_, statErr := os.Stat(host)
		views = append(views, hubSharedDirView{
			Name:          dir.Name,
			ReadOnly:      dir.ReadOnly,
			InWorkspace:   dir.InWorkspace,
			Mount:         mount,
			LocalHostPath: host,
			Exists:        statErr == nil,
			ProjectID:     hubCtx.ProjectID,
		})
	}
	return views, nil
}

func commandContext(cmd *cobra.Command) context.Context {
	if cmd != nil {
		if ctx := cmd.Context(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

func runHubSharedDirList(cmd *cobra.Command) error {
	hubCtx, err := requireHubSharedDirProject()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(commandContext(cmd), 30*time.Second)
	defer cancel()
	views, err := hubSharedDirViews(ctx, hubCtx)
	if err != nil {
		return wrapHubError(err)
	}
	if isJSONOutput() {
		return outputJSON(views)
	}
	if len(views) == 0 {
		fmt.Println("No shared directories configured.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tMODE\tMOUNT\tLOCAL_HOST\tPROVISIONED")
	for _, view := range views {
		mode := "read-write"
		if view.ReadOnly {
			mode = "read-only"
		}
		provisioned := "no"
		if view.Exists {
			provisioned = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", view.Name, mode, view.Mount, view.LocalHostPath, provisioned)
	}
	return tw.Flush()
}

func runHubSharedDirCreate(cmd *cobra.Command, name string, readOnly, inWorkspace bool) error {
	if err := api.ValidateSharedDirs([]api.SharedDir{{Name: name}}); err != nil {
		return err
	}
	hubCtx, err := requireHubSharedDirProject()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(commandContext(cmd), 30*time.Second)
	defer cancel()
	created, err := hubCtx.Client.Projects().CreateSharedDir(ctx, hubCtx.ProjectID, api.SharedDir{
		Name:        name,
		ReadOnly:    readOnly,
		InWorkspace: inWorkspace,
	})
	if err != nil {
		return wrapHubError(err)
	}
	mount := projectfs.SharedDirTarget(created.Name, created.InWorkspace, "")
	if isJSONOutput() {
		return outputJSON(map[string]interface{}{
			"status":       "success",
			"command":      "shared-dir create",
			"project_id":   hubCtx.ProjectID,
			"name":         created.Name,
			"read_only":    created.ReadOnly,
			"in_workspace": created.InWorkspace,
			"mount":        mount,
		})
	}
	fmt.Printf("Shared directory '%s' declared on project %s.\n", created.Name, hubCtx.ProjectID)
	fmt.Printf("Mount: %s\n", mount)
	return nil
}

func runHubSharedDirInfo(cmd *cobra.Command, name string) error {
	hubCtx, err := requireHubSharedDirProject()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(commandContext(cmd), 30*time.Second)
	defer cancel()
	views, err := hubSharedDirViews(ctx, hubCtx)
	if err != nil {
		return wrapHubError(err)
	}
	var view *hubSharedDirView
	for i := range views {
		if views[i].Name == name {
			view = &views[i]
			break
		}
	}
	if view == nil {
		return fmt.Errorf("shared directory %q not found", name)
	}
	if isJSONOutput() {
		return outputJSON(view)
	}
	fmt.Printf("Name:         %s\n", view.Name)
	if view.ReadOnly {
		fmt.Println("Mode:         read-only")
	} else {
		fmt.Println("Mode:         read-write")
	}
	fmt.Printf("Mount:        %s\n", view.Mount)
	fmt.Printf("Local host:   %s\n", view.LocalHostPath)
	fmt.Printf("Project:      %s\n", view.ProjectID)
	if view.Exists {
		fmt.Println("Provisioned:  yes")
	} else {
		fmt.Println("Provisioned:  no")
	}
	return nil
}

func runHubSharedDirRemove(cmd *cobra.Command, name string) error {
	if !autoConfirm {
		fmt.Printf("Remove shared directory '%s' from the hub project? [y/N] ", name)
		var response string
		_, _ = fmt.Scanln(&response)
		if response != "y" && response != "Y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}
	hubCtx, err := requireHubSharedDirProject()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(commandContext(cmd), 30*time.Second)
	defer cancel()
	if err := hubCtx.Client.Projects().DeleteSharedDir(ctx, hubCtx.ProjectID, name); err != nil {
		return wrapHubError(err)
	}
	if isJSONOutput() {
		return outputJSON(ActionResult{
			Status:  "success",
			Command: "shared-dir remove",
			Message: fmt.Sprintf("Shared directory '%s' removed from project %s.", name, hubCtx.ProjectID),
		})
	}
	fmt.Printf("Shared directory '%s' removed from project %s.\n", name, hubCtx.ProjectID)
	return nil
}

func init() {
	rootCmd.AddCommand(sharedDirCmd)
	sharedDirCmd.AddCommand(sharedDirListCmd)
	sharedDirCmd.AddCommand(sharedDirCreateCmd)
	sharedDirCmd.AddCommand(sharedDirRemoveCmd)
	sharedDirCmd.AddCommand(sharedDirInfoCmd)

	sharedDirCreateCmd.Flags().Bool("read-only", false, "Mount as read-only for agents")
	sharedDirCreateCmd.Flags().Bool("in-workspace", false, "Mount inside the workspace tree instead of /scion-volumes")
}
