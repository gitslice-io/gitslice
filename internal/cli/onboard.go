package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gitslice.io/gitslice/internal/objectid"
	"gitslice.io/gitslice/internal/usernames"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

type onboardOptions struct {
	serverAddr   string
	webURL       string
	email        string
	joinToken    string
	username     string
	displayName  string
	slice        string
	role         string
	installSkill bool
	dir          string
}

type onboardOutput struct {
	Status               string   `json:"status"`
	ServerAddr           string   `json:"server_addr"`
	SubjectID            string   `json:"subject_id"`
	Account              string   `json:"account"`
	AlreadyAuthenticated bool     `json:"already_authenticated"`
	ClaimURL             string   `json:"claim_url,omitempty"`
	HumanHandoffMessage  string   `json:"human_handoff_message,omitempty"`
	RequestedSlice       string   `json:"requested_slice,omitempty"`
	RequestedRole        string   `json:"requested_role,omitempty"`
	SliceAccess          string   `json:"slice_access,omitempty"`
	WorkspaceDir         string   `json:"workspace_dir,omitempty"`
	SkillInstalled       []string `json:"skill_installed,omitempty"`
	NextCommands         []string `json:"next_commands,omitempty"`
}

func (r Runner) onboardCommand(opts *commandOptions) *cobra.Command {
	in := onboardOptions{
		serverAddr: defaultServerAddr(),
		webURL:     defaultWebURL(),
	}
	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Idempotent one-shot onboarding for coding agents (register or join, check access, install skill)",
		Long: `Onboard a coding agent in one idempotent command:
  - With --join <gsi_...>: redeems a human-minted single-use invite token, links
    ownership, and grants slice access immediately.
  - Otherwise, if already authenticated, reuses the existing agent identity.
  - Otherwise, self-registers with --email (auto-generating a valid unique
    username if --username is omitted) and optionally requests access to --slice.
  - Optionally installs the Gitslice Agent Skill (--install-skill) and checks
    access to --slice (or initializes a workspace in --dir).`,
		Args: noArgs("gs onboard [--email OWNER_EMAIL | --join TOKEN] [--username NAME] [--slice account/slice] [--role writer] [--install-skill] [--dir DIR] [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runOnboard(cmd.Context(), *opts, in)
		},
	}
	cmd.Flags().StringVar(&in.serverAddr, "server", in.serverAddr, "server gRPC address")
	cmd.Flags().StringVar(&in.webURL, "web-url", in.webURL, "web app base URL")
	cmd.Flags().StringVar(&in.email, "email", "", "email of the human owner (for self-registration)")
	cmd.Flags().StringVar(&in.joinToken, "join", "", "single-use human-minted invite token (gsi_...) or '-' for stdin")
	cmd.Flags().StringVar(&in.username, "username", "", "desired agent username (optional; auto-generated when omitted)")
	cmd.Flags().StringVar(&in.displayName, "display-name", "", "human-readable agent display name")
	cmd.Flags().StringVar(&in.slice, "slice", "", "target slice (account/slice) to request or check access for")
	cmd.Flags().StringVar(&in.role, "role", "", "requested role on --slice (reader, contributor, writer, admin; default writer)")
	cmd.Flags().BoolVar(&in.installSkill, "install-skill", false, "install the canonical Gitslice SKILL.md into detected agent directories")
	cmd.Flags().StringVar(&in.dir, "dir", "", "optional empty directory to initialize the target slice workspace into")
	withSchemaOutput[onboardOutput](cmd)
	return cmd
}

func (r Runner) runOnboard(ctx context.Context, opts commandOptions, in onboardOptions) error {
	out := onboardOutput{
		ServerAddr: strings.TrimSpace(in.serverAddr),
	}
	if out.ServerAddr == "" {
		out.ServerAddr = defaultServerAddr()
	}

	targetSlice := strings.TrimSpace(in.slice)
	if strings.TrimSpace(in.joinToken) != "" || (strings.TrimSpace(in.email) == "" && strings.TrimSpace(r.Getenv("GS_JOIN_TOKEN")) != "") {
		joined, err := r.joinAgentInvite(ctx, joinAgentInviteOptions{
			serverAddr:  out.ServerAddr,
			token:       in.joinToken,
			username:    in.username,
			displayName: in.displayName,
		})
		if err != nil {
			return err
		}
		out.Status = "joined"
		out.ServerAddr = joined.ServerAddr
		out.SubjectID = joined.AgentSubjectID
		out.Account = joined.Account
		if joined.Slice != "" {
			if targetSlice == "" {
				targetSlice = joined.Slice
			}
			out.SliceAccess = fmt.Sprintf("granted (%s on %s)", joined.Role, joined.Slice)
		}
	} else if existing, ok := r.checkExistingAuth(ctx); ok {
		out.Status = "already_authenticated"
		out.AlreadyAuthenticated = true
		out.ServerAddr = existing.serverAddr
		out.SubjectID = existing.subjectID
		out.Account = existing.account
	} else {
		if strings.TrimSpace(in.email) == "" {
			return userError("invalid_args", "pass --email OWNER_EMAIL to self-register or --join TOKEN to redeem an invite",
				"Run gs onboard --email owner@example.com --slice account/slice --json, or gs onboard --join gsi_... --json.")
		}
		username := strings.TrimSpace(in.username)
		if username == "" {
			var err error
			username, err = generateAgentUsername()
			if err != nil {
				return err
			}
		}
		regOut, err := r.registerAgent(ctx, opts, registerAgentOptions{
			serverAddr:     out.ServerAddr,
			webURL:         in.webURL,
			username:       username,
			email:          in.email,
			displayName:    in.displayName,
			requestedSlice: targetSlice,
			requestedRole:  in.role,
		})
		if err != nil {
			return err
		}
		out.Status = "registered"
		out.ServerAddr = regOut.ServerAddr
		out.SubjectID = regOut.SubjectID
		out.Account = regOut.Account
		out.ClaimURL = regOut.ClaimURL
		out.HumanHandoffMessage = regOut.ClaimInstructions
		out.RequestedSlice = regOut.RequestedSlice
		out.RequestedRole = regOut.RequestedRole
	}

	if in.installSkill {
		res, err := r.installSkill("auto", false)
		if err != nil {
			return err
		}
		out.SkillInstalled = res.Installed
	}

	if targetSlice != "" && out.SliceAccess == "" {
		if err := r.checkSliceAccess(ctx, targetSlice); err == nil {
			out.SliceAccess = "accessible"
		} else {
			out.SliceAccess = "pending_access"
		}
	}

	if strings.TrimSpace(in.dir) != "" && targetSlice != "" && out.SliceAccess != "pending_access" {
		if err := os.MkdirAll(in.dir, 0o755); err != nil {
			return err
		}
		sub := r
		sub.Dir = in.dir
		subOpts := opts
		subOpts.Quiet = true
		subOpts.Format = "text"
		subOpts.JSONFields = nil
		subOpts.JQ = ""
		subOpts.Template = ""
		if err := sub.runWorkspaceInit(ctx, subOpts, targetSlice, ""); err != nil {
			return err
		}
		out.WorkspaceDir = in.dir
	}

	if targetSlice != "" {
		if out.SliceAccess == "pending_access" {
			out.NextCommands = append(out.NextCommands,
				fmt.Sprintf("gs doctor %s", targetSlice),
				fmt.Sprintf("gs init %s", targetSlice),
			)
		} else if out.WorkspaceDir == "" {
			out.NextCommands = append(out.NextCommands, fmt.Sprintf("gs init %s", targetSlice))
		}
	} else if out.Account != "" {
		out.NextCommands = append(out.NextCommands, fmt.Sprintf("gs init %s/home", out.Account))
	}
	if !in.installSkill {
		out.NextCommands = append(out.NextCommands, "gs skill install")
	}

	if opts.JSONOutput() {
		return r.writeJSONOutput(opts, out)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.Stdout, "onboard status: %s (account %s, subject %s)\n", out.Status, out.Account, out.SubjectID)
	if out.HumanHandoffMessage != "" {
		fmt.Fprintf(r.Stdout, "\nTell your owner how to claim this agent:\n%s\n", out.HumanHandoffMessage)
	}
	if out.SliceAccess != "" {
		fmt.Fprintf(r.Stdout, "slice access (%s): %s\n", targetSlice, out.SliceAccess)
	}
	if out.WorkspaceDir != "" {
		fmt.Fprintf(r.Stdout, "initialized workspace in %s\n", out.WorkspaceDir)
	}
	for _, p := range out.SkillInstalled {
		fmt.Fprintf(r.Stdout, "installed gitslice skill to %s\n", p)
	}
	if len(out.NextCommands) > 0 {
		fmt.Fprintln(r.Stdout, "\nNext steps:")
		for _, c := range out.NextCommands {
			fmt.Fprintf(r.Stdout, "  %s\n", c)
		}
	}
	return nil
}

type existingAuthSummary struct {
	serverAddr string
	subjectID  string
	account    string
}

func (r Runner) checkExistingAuth(ctx context.Context) (existingAuthSummary, bool) {
	cfg, err := r.readUserConfig()
	if err != nil || strings.TrimSpace(cfg.Token) == "" {
		return existingAuthSummary{}, false
	}
	conn, err := r.dialServer(ctx, cfg.ServerAddr)
	if err != nil {
		return existingAuthSummary{}, false
	}
	defer conn.Close()
	resp, err := corev1.NewAuthServiceClient(conn).GetAuthStatus(authContext(ctx, cfg), &corev1.GetAuthStatusRequest{})
	if err != nil || strings.TrimSpace(resp.GetSubjectId()) == "" {
		return existingAuthSummary{}, false
	}
	account := ""
	if len(resp.GetAccounts()) > 0 {
		account = resp.GetAccounts()[0]
	}
	return existingAuthSummary{
		serverAddr: cfg.ServerAddr,
		subjectID:  resp.GetSubjectId(),
		account:    account,
	}, true
}

func (r Runner) checkSliceAccess(ctx context.Context, sliceInput string) error {
	return r.withSession(ctx, func(s *Session) error {
		ref, err := r.resolveSliceRefInput(s.Ctx(), s.Config(), s.Conn(), sliceInput)
		if err != nil {
			return err
		}
		_, err = s.Slice().ResolveSlice(s.Ctx(), &corev1.ResolveSliceRequest{Ref: ref})
		return err
	})
}

func generateAgentUsername() (string, error) {
	host, _ := os.Hostname()
	host = strings.ToLower(strings.TrimSpace(host))
	var b strings.Builder
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == '-' || r == '_' || r == '.' {
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 16 {
		slug = strings.TrimRight(slug[:16], "-")
	}
	suffix, err := objectid.RandomID("a")
	if err != nil {
		return "", err
	}
	hexPart := strings.TrimPrefix(suffix, "a_")
	if len(hexPart) > 6 {
		hexPart = hexPart[:6]
	}
	candidate := "agent-" + hexPart
	if slug != "" {
		candidate = "agent-" + slug + "-" + hexPart
	}
	return usernames.Normalize(candidate)
}
