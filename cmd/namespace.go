package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/service"
)

var namespaceCmd = &cobra.Command{
	Use:     "namespace",
	Aliases: []string{"ns"},
	Short:   "Manage namespaces",
}

func newNamespaceCreateCmd() *cobra.Command {
	var owners []string
	var company, email, description string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a namespace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			ns, err := a.svc.CreateNamespace(cmd.Context(), service.System, args[0], owners)
			if err != nil {
				return err
			}
			if company != "" || email != "" || description != "" {
				if _, err := a.svc.UpdateNamespace(cmd.Context(), ns.Name, func(n *model.Namespace) error {
					n.Company, n.Email, n.Description = company, email, description
					return nil
				}); err != nil {
					return err
				}
			}
			fmt.Printf("Created namespace %s (id %d)\n", ns.Name, ns.ID)
			return nil
		},
	}
	cmd.Flags().StringSliceVarP(&owners, "owner", "o", nil, "owner username (repeatable)")
	cmd.Flags().StringVar(&company, "company", "", "company name")
	cmd.Flags().StringVar(&email, "email", "", "contact email")
	cmd.Flags().StringVar(&description, "description", "", "description")
	return cmd
}

func newNamespaceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List namespaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			all, err := a.svc.ListNamespaces(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tOWNERS\tCOMPANY")
			for _, ns := range all {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", ns.ID, ns.Name, strings.Join(ns.Owners, ","), ns.Company)
			}
			return tw.Flush()
		},
	}
}

func newNamespaceOwnerCmd(add bool) *cobra.Command {
	use, short := "add-owner <namespace> <username>...", "Add owners to a namespace"
	if !add {
		use, short = "remove-owner <namespace> <username>...", "Remove owners from a namespace"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			var ns *model.Namespace
			if add {
				ns, err = a.svc.AddNamespaceOwners(cmd.Context(), args[0], args[1:]...)
			} else {
				ns, err = a.svc.RemoveNamespaceOwners(cmd.Context(), args[0], args[1:]...)
			}
			if err != nil {
				return err
			}
			fmt.Printf("Owners of %s: %s\n", ns.Name, strings.Join(ns.Owners, ", "))
			return nil
		},
	}
}

func newNamespaceDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete an empty namespace",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			if err := a.svc.DeleteNamespace(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Deleted namespace %s\n", args[0])
			return nil
		},
	}
}

func init() {
	namespaceCmd.AddCommand(newNamespaceCreateCmd(), newNamespaceListCmd(), newNamespaceOwnerCmd(true),
		newNamespaceOwnerCmd(false), newNamespaceDeleteCmd())
	rootCmd.AddCommand(namespaceCmd)
}
