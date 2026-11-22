package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
)

// accountCommand groups organization account management.
func (r Runner) accountCommand(opts *commandOptions) *cobra.Command {
	accountCmd := &cobra.Command{
		Use:   "account",
		Short: "Manage accounts: organizations, members, invitations, profiles and the active account",
		RunE:  requireSubcommand("account"),
	}
	markSchemaGroup(accountCmd)
	var owners []string
	createOrgCmd := &cobra.Command{
		Use:   "create-org <slug>",
		Short: "Create an organization account; you become its owner",
		Args:  exactArgs(1, "gs account create-org <slug> [--owner <username>]..."),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountCreateOrg(cmd.Context(), *opts, args[0], owners)
		},
	}
	createOrgCmd.Flags().StringArrayVar(&owners, "owner", nil, "username of an initial owner; repeat for several (operators only; default: you)")
	membersCmd := &cobra.Command{
		Use:   "members <account>",
		Short: "List an account's members and their roles",
		Args:  exactArgs(1, "gs account members <account>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountMembers(cmd.Context(), *opts, args[0])
		},
	}
	role := ""
	setMemberCmd := &cobra.Command{
		Use:   "set-member <account> <username>",
		Short: "Change a member's role (operators may also add members directly)",
		Args:  exactArgs(2, "gs account set-member <account> <username> --role owner|admin|writer|member|contributor|reader"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountSetMember(cmd.Context(), *opts, args[0], args[1], role)
		},
	}
	setMemberCmd.Flags().StringVar(&role, "role", role, "role: owner, admin, writer, member, contributor or reader")
	removeYes := false
	removeMemberCmd := &cobra.Command{
		Use:   "remove-member <account> <username>",
		Short: "Remove a member from an organization",
		Args:  exactArgs(2, "gs account remove-member <account> <username> --yes"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountRemoveMember(cmd.Context(), *opts, args[0], args[1], removeYes)
		},
	}
	removeMemberCmd.Flags().BoolVar(&removeYes, "yes", removeYes, "confirm removing the member")
	inviteRole := "writer"
	inviteCmd := &cobra.Command{
		Use:   "invite <org> <username>",
		Short: "Invite a user to an organization; they join when they accept",
		Args:  exactArgs(2, "gs account invite <org> <username> [--role writer]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountInvite(cmd.Context(), *opts, args[0], args[1], inviteRole)
		},
	}
	inviteCmd.Flags().StringVar(&inviteRole, "role", inviteRole, "role: owner, admin, writer, member, contributor or reader")
	invitationsCmd := &cobra.Command{
		Use:   "invitations [<org>]",
		Short: "List your pending invitations, or an organization's",
		Args:  maxArgs(1, "gs account invitations [<org>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := ""
			if len(args) == 1 {
				org = args[0]
			}
			return r.runAccountInvitations(cmd.Context(), *opts, org)
		},
	}
	acceptCmd := &cobra.Command{
		Use:   "accept <org>",
		Short: "Accept your invitation to an organization",
		Args:  exactArgs(1, "gs account accept <org>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountRespond(cmd.Context(), *opts, args[0], true)
		},
	}
	declineCmd := &cobra.Command{
		Use:   "decline <org>",
		Short: "Decline your invitation to an organization",
		Args:  exactArgs(1, "gs account decline <org>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountRespond(cmd.Context(), *opts, args[0], false)
		},
	}
	cancelInviteCmd := &cobra.Command{
		Use:   "cancel-invite <org> <username>",
		Short: "Withdraw a pending invitation",
		Args:  exactArgs(2, "gs account cancel-invite <org> <username>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountCancelInvite(cmd.Context(), *opts, args[0], args[1])
		},
	}
	clearActive := false
	useCmd := &cobra.Command{
		Use:   "use [<account>]",
		Short: "Make an account you belong to the default for commands (like gs slice list)",
		Args:  maxArgs(1, "gs account use <account> | gs account use --clear"),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := ""
			if len(args) == 1 {
				account = args[0]
			}
			return r.runAccountUse(cmd.Context(), *opts, account, clearActive)
		},
	}
	useCmd.Flags().BoolVar(&clearActive, "clear", false, "go back to your personal account")
	currentCmd := &cobra.Command{
		Use:   "current",
		Short: "Show the account commands default to",
		Args:  exactArgs(0, "gs account current"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountCurrent(cmd.Context(), *opts)
		},
	}
	profileCmd := &cobra.Command{
		Use:   "profile <account>",
		Short: "Show an account's public profile",
		Args:  exactArgs(1, "gs account profile <account>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAccountProfile(cmd.Context(), *opts, args[0])
		},
	}
	var profileName, profileDescription, profileWebsite string
	setProfileCmd := &cobra.Command{
		Use:   "set-profile <account>",
		Short: "Change an account's display name, description or website",
		Args:  exactArgs(1, "gs account set-profile <account> [--name ...] [--description ...] [--website ...]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name, description, website *string
			if cmd.Flags().Changed("name") {
				name = &profileName
			}
			if cmd.Flags().Changed("description") {
				description = &profileDescription
			}
			if cmd.Flags().Changed("website") {
				website = &profileWebsite
			}
			return r.runAccountSetProfile(cmd.Context(), *opts, args[0], name, description, website)
		},
	}
	setProfileCmd.Flags().StringVar(&profileName, "name", "", "display name (at most 64 characters; empty clears it)")
	setProfileCmd.Flags().StringVar(&profileDescription, "description", "", "description (at most 280 characters)")
	setProfileCmd.Flags().StringVar(&profileWebsite, "website", "", "an http or https URL")

	ciRunnersCmd := &cobra.Command{
		Use:   "ci-runners [account]",
		Short: "List an account's default CI runner pool (inherited by all slices)",
		Args:  maxArgs(1, "gs account ci-runners [account]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			account := ""
			if len(args) == 1 {
				account = args[0]
			}
			return r.runAccountCIRunners(cmd.Context(), *opts, account)
		},
	}
	var ciDaemonAdd, ciDaemonRemove bool
	setCIDaemonCmd := &cobra.Command{
		Use:   "set-ci-daemon <account> <daemon-id>",
		Short: "Set an account's default CI runner, or add/remove one in its pool",
		Long: "Set the default CI runner pool for an account. Every slice under /<account> inherits this pool\n" +
			"unless it configures its own slice override. Pass --add to append a backup runner, --remove to\n" +
			"remove one, or \"\" as <daemon-id> to clear the pool.",
		Args: exactArgs(2, "gs account set-ci-daemon <account> <daemon-id> [--add|--remove]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ciDaemonAdd && ciDaemonRemove {
				return userError("invalid_args", "--add and --remove cannot be used together", "Run the command once for each.")
			}
			return r.runAccountSetCIDaemon(cmd.Context(), *opts, args[0], args[1], ciDaemonAdd, ciDaemonRemove)
		},
	}
	setCIDaemonCmd.Flags().BoolVar(&ciDaemonAdd, "add", false, "add the daemon to the account's CI runner pool")
	setCIDaemonCmd.Flags().BoolVar(&ciDaemonRemove, "remove", false, "remove the daemon from the account's CI runner pool")
	withSchemaOutput[accountCIRunnersOutput](ciRunnersCmd, setCIDaemonCmd)

	accountCmd.AddCommand(createOrgCmd, membersCmd, setMemberCmd, removeMemberCmd, inviteCmd, invitationsCmd, acceptCmd, declineCmd, cancelInviteCmd, useCmd, currentCmd, profileCmd, setProfileCmd, ciRunnersCmd, setCIDaemonCmd)
	return accountCmd
}

var accountErrorHints = domainErrorOverrides{
	codes.PermissionDenied: {
		Hint: "Members, invitations and organization profiles are managed by owners and admins.",
	},
	codes.NotFound: {
		Hint: "Check the account and username, and that you are a member.",
	},
	codes.AlreadyExists: {
		Hint: "Pick another name, or change an existing member's role with gs account set-member.",
	},
	codes.FailedPrecondition: {
		HintFn: func(message string) string {
			if strings.Contains(message, "invite them") {
				return "Run gs account invite <org> <username> --role <role>; they join when they accept."
			}
			return ""
		},
	},
	codes.ResourceExhausted: {
		Code: "limit_reached",
	},
}

func accountError(err error) error {
	return withDomainErrorHints(err, accountErrorHints)
}
