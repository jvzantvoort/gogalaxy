package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/service"
)

var userCmd = &cobra.Command{
	Use:   "user",
	Short: "Manage users",
}

// passwordInput reads a password from a flag, stdin or an interactive prompt.
type passwordInput struct {
	value string
	stdin bool
}

func (p *passwordInput) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p.value, "password", "", "password (insecure: visible in the process list)")
	cmd.Flags().BoolVar(&p.stdin, "password-stdin", false, "read the password from stdin")
}

// read returns the password; prompt controls interactive prompting.
func (p *passwordInput) read(prompt bool) (string, error) {
	switch {
	case p.value != "":
		return p.value, nil
	case p.stdin:
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	case !prompt || !term.IsTerminal(int(os.Stdin.Fd())):
		return "", nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Password (again): ")
	again, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(pw) != string(again) {
		return "", errors.New("passwords do not match")
	}
	return string(pw), nil
}

func newUserAddCmd() *cobra.Command {
	var in service.NewUser
	var pw passwordInput
	var token bool
	cmd := &cobra.Command{
		Use:   "add <username>",
		Short: "Create a user",
		Long: `Create a user. Without --password or --password-stdin the password is
prompted for when running interactively; users without a password can only
authenticate with an API token.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			in.Username = args[0]
			if in.Password, err = pw.read(true); err != nil {
				return err
			}
			u, err := a.svc.CreateUser(cmd.Context(), in)
			if err != nil {
				return err
			}
			fmt.Printf("Created user %s (id %d)\n", u.Username, u.ID)
			if token {
				t, err := a.svc.IssueToken(cmd.Context(), u.Username)
				if err != nil {
					return err
				}
				fmt.Printf("Token: %s\n", t)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&in.Email, "email", "", "email address")
	f.StringVar(&in.FirstName, "first-name", "", "first name")
	f.StringVar(&in.LastName, "last-name", "", "last name")
	f.BoolVar(&in.Superuser, "superuser", false, "grant full administrative access")
	f.BoolVar(&token, "token", false, "also create and print an API token")
	pw.register(cmd)
	return cmd
}

func newUserListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			users, err := a.svc.ListUsers(cmd.Context())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tUSERNAME\tEMAIL\tSUPERUSER\tACTIVE\tPASSWORD")
			for _, u := range users {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%t\t%t\t%t\n", u.ID, u.Username, u.Email, u.IsSuperuser, u.IsActive, u.PasswordHash != "")
			}
			return tw.Flush()
		},
	}
}

func newUserSetCmd() *cobra.Command {
	var email string
	var superuser, active bool
	cmd := &cobra.Command{
		Use:   "set <username>",
		Short: "Change user attributes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			f := cmd.Flags()
			_, err = a.svc.UpdateUser(cmd.Context(), args[0], func(u *model.User) error {
				if f.Changed("email") {
					u.Email = email
				}
				if f.Changed("superuser") {
					u.IsSuperuser = superuser
				}
				if f.Changed("active") {
					u.IsActive = active
				}
				return nil
			})
			if err != nil {
				return err
			}
			fmt.Printf("Updated user %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "email address")
	cmd.Flags().BoolVar(&superuser, "superuser", false, "grant (or with =false revoke) administrative access")
	cmd.Flags().BoolVar(&active, "active", true, "enable (or with =false disable) the account")
	return cmd
}

func newUserPasswdCmd() *cobra.Command {
	var pw passwordInput
	var clear bool
	cmd := &cobra.Command{
		Use:   "passwd <username>",
		Short: "Set the password of a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			password := ""
			if !clear {
				if password, err = pw.read(true); err != nil {
					return err
				}
				if password == "" {
					return errors.New("no password given (use --clear to remove the password)")
				}
			}
			if err := a.svc.SetPassword(cmd.Context(), args[0], password); err != nil {
				return err
			}
			fmt.Printf("Password of %s updated\n", args[0])
			return nil
		},
	}
	pw.register(cmd)
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the password (token login only)")
	return cmd
}

func newUserTokenCmd() *cobra.Command {
	var revoke bool
	cmd := &cobra.Command{
		Use:   "token <username>",
		Short: "Create (replacing any existing) or revoke the API token of a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			if revoke {
				if err := a.svc.RevokeToken(cmd.Context(), args[0]); err != nil {
					return err
				}
				fmt.Printf("Token of %s revoked\n", args[0])
				return nil
			}
			t, err := a.svc.IssueToken(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Println(t)
			return nil
		},
	}
	cmd.Flags().BoolVar(&revoke, "revoke", false, "revoke the token instead of creating one")
	return cmd
}

func newUserDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <username>",
		Aliases: []string{"rm"},
		Short:   "Delete a user",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp()
			if err != nil {
				return err
			}
			defer a.Close()
			if err := a.svc.DeleteUser(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Deleted user %s\n", args[0])
			return nil
		},
	}
}

func init() {
	userCmd.AddCommand(newUserAddCmd(), newUserListCmd(), newUserSetCmd(), newUserPasswdCmd(),
		newUserTokenCmd(), newUserDeleteCmd())
	rootCmd.AddCommand(userCmd)
}
