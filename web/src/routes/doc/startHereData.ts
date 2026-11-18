export const startSteps = [
  {
    title: "Install gs",
    description:
      "One command for Linux and macOS: downloads the latest prebuilt binary, verifies its checksum, and installs it to ~/.local/bin. For Windows, go install, or building from source, see the CLI Reference.",
    command: "curl -fsSL https://gitslice.io/install.sh | sh\ngs version"
  },
  {
    title: "Sign in",
    description: "Authenticate once, then use the same identity for CLI and web.",
    command: "gs auth login\ngs auth status"
  },
  {
    title: "Open your home slice",
    description:
      "Your personal account has a home slice rooted at /<you>. Use it for a first workflow. For a big slice, gs init --lazy <slice> <dir> mounts it instead of downloading everything (Linux and macOS, with FUSE); see Lazy Workspaces.",
    command: "gs init <you>:home\n# example: gs init nic:home"
  },
  {
    title: "Make a local edit",
    description: "Edit files normally, then inspect what the workspace will send.",
    command: "gs status\ngs diff"
  },
  {
    title: "Create and submit a changeset",
    description:
      "A changeset is the review and submit unit. When it lands, it is included in the accepted main tree.",
    command: 'gs create --message "my first change"\ngs submit\ngs status'
  }
];
