export interface StartStep {
  command: string;
  description: string;
  label: string;
  note?: string;
  title: string;
}

export const startSteps: StartStep[] = [
  {
    title: "Install gs",
    label: "install commands",
    description:
      "Downloads the latest checksum-verified binary for Linux, macOS, or Windows POSIX shells (Git Bash / MSYS2) and installs it to ~/.local/bin (falls back to building from source if Go is installed).",
    command: "curl -fsSL https://gitslice.io/install.sh | sh\ngs version",
    note: 'If your shell says command not found, add ~/.local/bin to PATH: export PATH="$HOME/.local/bin:$PATH".'
  },
  {
    title: "Sign in",
    label: "sign-in commands",
    description:
      "Authenticate once in your browser; the CLI and web app share the same identity. Coding agents can sign up without a browser via gs auth register-agent.",
    command: "gs auth login\ngs auth status"
  },
  {
    title: "Initialize your home slice",
    label: "workspace initialization commands",
    description:
      "Every account has a private home slice rooted at /<you>. Pass a new or empty directory to gs init (both <you>/home and <you>:home are accepted). Inside the workspace, files sit at their full account-rooted path under <you>/.",
    command: "gs init <you>/home ~/work/home\ncd ~/work/home\n# example: gs init nic/home ~/work/home",
    note: "Working on a large slice on Linux or macOS with FUSE? Pass --lazy to mount it in seconds and fetch files on first read."
  },
  {
    title: "Edit files and inspect changes",
    label: "edit and inspect commands",
    description:
      "Create or edit files under your account directory with any editor, then check what the workspace will include.",
    command: 'echo "hello gitslice" > <you>/HELLO.md\ngs status\ngs diff'
  },
  {
    title: "Create and submit a changeset",
    label: "changeset create and submit commands",
    description:
      "Changesets are the review and submit unit. Submitting validates required checks and approvals, then lands your edits as an immutable native commit on refs/global/main.",
    command:
      'gs create --all --message "add greeting"\ngs submit\ngs status\n\n# shorthand: create, wait for required checks, and submit in one step\ngs create --all --message "add greeting" --submit',
    note: "Run gs sync to pull newer changes from main, gs browse --print <you>/HELLO.md to get a verified web URL, or gs doctor if anything looks off."
  }
];
