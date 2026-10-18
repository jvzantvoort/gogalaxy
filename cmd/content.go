package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jvzantvoort/gogalaxy/internal/importer"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/service"
)

var collectionCmd = &cobra.Command{
	Use:     "collection",
	Aliases: []string{"col"},
	Short:   "Manage collections",
}

var roleCmd = &cobra.Command{
	Use:   "role",
	Short: "Manage (legacy) roles",
}

func repoFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVarP(target, "repository", "r", "", "repository (default: content.upload_repository / content.default_repository)")
}

func newCollectionImportCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "import <namespace-name-version.tar.gz>...",
		Short: "Import collection artifacts built with 'ansible-galaxy collection build'",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			if repo == "" {
				repo = a.cfg.Content.UploadRepository
			}
			failed := 0
			for _, file := range args {
				if err := importCollection(cmd, a, repo, file); err != nil {
					fmt.Fprintf(os.Stderr, "%s: %v\n", file, err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d imports failed", failed, len(args))
			}
			return nil
		},
	}
	repoFlag(cmd, &repo)
	return cmd
}

func importCollection(cmd *cobra.Command, a *app, repo, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	task, err := a.svc.PublishCollection(cmd.Context(), service.System, service.PublishRequest{
		Repository: repo, Filename: filepath.Base(file), Body: f,
	})
	if err != nil {
		return err
	}
	if task.State != model.TaskCompleted {
		return errors.New(task.Error.Description)
	}
	fmt.Printf("Imported %s.%s %s into %s\n", task.Namespace, task.Name, task.Version, repo)
	return nil
}

func newCollectionListCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List collections and their versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			repos := a.cfg.Content.Repositories
			if repo != "" {
				repos = []string{repo}
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "REPOSITORY\tCOLLECTION\tVERSIONS\tDEPRECATED")
			for _, r := range repos {
				cols, err := a.svc.ListCollections(cmd.Context(), service.System, r)
				if err != nil {
					return err
				}
				for _, c := range cols {
					versions, err := a.svc.ListVersions(cmd.Context(), service.System, r, c.Namespace, c.Name)
					if err != nil {
						return err
					}
					names := make([]string, 0, len(versions))
					for _, v := range versions {
						names = append(names, v.Version)
					}
					fmt.Fprintf(tw, "%s\t%s.%s\t%s\t%t\n", r, c.Namespace, c.Name, strings.Join(names, ", "), c.Deprecated)
				}
			}
			return tw.Flush()
		},
	}
	repoFlag(cmd, &repo)
	return cmd
}

func newCollectionDeleteCmd() *cobra.Command {
	var repo, version string
	cmd := &cobra.Command{
		Use:     "delete <namespace.name>",
		Aliases: []string{"rm"},
		Short:   "Delete a collection, or one version of it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns, name, err := splitFQCN(args[0])
			if err != nil {
				return err
			}
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			if repo == "" {
				repo = a.cfg.Content.DefaultRepository
			}
			if version != "" {
				err = a.svc.DeleteCollectionVersion(cmd.Context(), service.System, repo, ns, name, version)
			} else {
				err = a.svc.DeleteCollection(cmd.Context(), service.System, repo, ns, name)
			}
			if err != nil {
				return err
			}
			fmt.Printf("Deleted %s %s from %s\n", args[0], version, repo)
			return nil
		},
	}
	repoFlag(cmd, &repo)
	cmd.Flags().StringVar(&version, "version", "", "only delete this version")
	return cmd
}

func newCollectionDeprecateCmd() *cobra.Command {
	var repo string
	var undo bool
	cmd := &cobra.Command{
		Use:   "deprecate <namespace.name>",
		Short: "Mark a collection as deprecated",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns, name, err := splitFQCN(args[0])
			if err != nil {
				return err
			}
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			if repo == "" {
				repo = a.cfg.Content.DefaultRepository
			}
			if _, err := a.svc.SetDeprecated(cmd.Context(), service.System, repo, ns, name, !undo); err != nil {
				return err
			}
			fmt.Printf("%s deprecated=%t in %s\n", args[0], !undo, repo)
			return nil
		},
	}
	repoFlag(cmd, &repo)
	cmd.Flags().BoolVar(&undo, "undo", false, "remove the deprecation")
	return cmd
}

func newRoleImportCmd() *cobra.Command {
	var in service.RoleImport
	cmd := &cobra.Command{
		Use:   "import <archive.tar.gz|directory>",
		Short: "Import a role from an archive or a role directory",
		Long: `Import a role version. The namespace and name default to galaxy_info.namespace
and galaxy_info.role_name from meta/main.yml.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if in.Version == "" {
				return errors.New("--version is required")
			}
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			body, err := roleSource(args[0], in)
			if err != nil {
				return err
			}
			in.Body = body
			role, err := a.svc.ImportRole(cmd.Context(), service.System, in)
			if err != nil {
				return err
			}
			fmt.Printf("Imported role %s.%s %s (id %d)\n", role.Namespace, role.Name, in.Version, role.ID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&in.Namespace, "namespace", "n", "", "role namespace (github_user)")
	f.StringVar(&in.Name, "name", "", "role name")
	f.StringVarP(&in.Version, "version", "V", "", "version (tag) of this release, e.g. 1.0.0")
	f.StringVar(&in.GithubRepo, "github-repo", "", "informational source repository name")
	return cmd
}

// roleSource opens a role archive, or packs a role directory in memory.
func roleSource(path string, in service.RoleImport) (io.Reader, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return os.Open(path)
	}
	prefix := filepath.Base(filepath.Clean(path))
	if in.Namespace != "" && in.Name != "" {
		prefix = in.Namespace + "." + in.Name
	}
	var buf bytes.Buffer
	if err := importer.PackDir(path, prefix+"-"+in.Version, &buf); err != nil {
		return nil, err
	}
	return &buf, nil
}

func newRoleListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List roles and their versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			roles, err := a.svc.ListRoles(cmd.Context(), service.System, service.RoleFilter{})
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tROLE\tVERSIONS\tDOWNLOADS")
			for _, r := range roles {
				names := make([]string, 0, len(r.Versions))
				for _, v := range r.Versions {
					names = append(names, v.Name)
				}
				fmt.Fprintf(tw, "%d\t%s.%s\t%s\t%d\n", r.ID, r.Namespace, r.Name, strings.Join(names, ", "), r.DownloadCount)
			}
			return tw.Flush()
		},
	}
}

func newRoleDeleteCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:     "delete <namespace.name>",
		Aliases: []string{"rm"},
		Short:   "Delete a role, or one version of it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns, name, err := splitFQCN(args[0])
			if err != nil {
				return err
			}
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			if err := a.svc.DeleteRole(cmd.Context(), service.System, ns, name, version); err != nil {
				return err
			}
			fmt.Printf("Deleted role %s %s\n", args[0], version)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "only delete this version")
	return cmd
}

func init() {
	collectionCmd.AddCommand(newCollectionImportCmd(), newCollectionListCmd(), newCollectionDeleteCmd(),
		newCollectionDeprecateCmd())
	roleCmd.AddCommand(newRoleImportCmd(), newRoleListCmd(), newRoleDeleteCmd())
	rootCmd.AddCommand(collectionCmd, roleCmd)
}
