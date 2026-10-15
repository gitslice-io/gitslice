package cli

// schemaFileCommands describes gs fs.
func schemaFileCommands() []map[string]any {
	return []map[string]any{
		{
			"use":            "gs fs ls [remote-path]",
			"summary":        "list a remote directory or file in the signed-in home slice",
			"args":           []string{"remote-path"},
			"writes_stdout":  true,
			"machine_output": []string{"path", "slice", "commit_id", "entries"},
		},
		{
			"use":            "gs fs cat <absolute-path>",
			"summary":        "print a remote file under the signed-in home slice",
			"args":           []string{"absolute-path"},
			"writes_stdout":  true,
			"machine_output": []string{"path", "slice", "commit_id", "content_hash", "data_base64"},
		},
		{
			"use":            "gs fs mkdir <absolute-path>",
			"summary":        "create a remote directory under the signed-in home slice",
			"args":           []string{"absolute-path"},
			"writes_stdout":  true,
			"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
		},
		{
			"use":            "gs fs write <absolute-path> (--text text|--stdin)",
			"summary":        "create or replace a remote file under the signed-in home slice",
			"args":           []string{"absolute-path"},
			"flags":          []string{"--text", "--stdin"},
			"writes_stdout":  true,
			"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
		},
		{
			"use":            "gs fs touch <absolute-path>",
			"summary":        "create an empty remote file under the signed-in home slice",
			"args":           []string{"absolute-path"},
			"writes_stdout":  true,
			"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
		},
		{
			"use":            "gs fs upload <local-path> <absolute-remote-path>",
			"summary":        "upload a local file or directory under the signed-in home slice",
			"args":           []string{"local-path", "absolute-remote-path"},
			"flags":          []string{"--recursive", "--concurrency"},
			"writes_stdout":  true,
			"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
		},
		{
			"use":            "gs fs mv <absolute-old-path> <absolute-new-path>",
			"summary":        "rename or move a remote file or directory under the signed-in home slice",
			"args":           []string{"absolute-old-path", "absolute-new-path"},
			"writes_stdout":  true,
			"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
		},
		{
			"use":            "gs fs rm <absolute-path>",
			"summary":        "delete a remote file or directory under the signed-in home slice",
			"args":           []string{"absolute-path"},
			"writes_stdout":  true,
			"machine_output": []string{"operation", "slice", "commit_id", "new_ref_commit_id", "changed_paths"},
		},
	}
}

// schemaToolCommands describes import, shell, version, schema, completion and help.
func schemaToolCommands() []map[string]any {
	return []map[string]any{
		{
			"use":            "gs import <source>",
			"summary":        "import a Git repository under a mounted path",
			"args":           []string{"source"},
			"flags":          []string{"--mount", "--slice", "--mode", "--deep", "--max-commits", "--resume"},
			"writes_stdout":  true,
			"machine_output": []string{"source", "mount_path", "mode", "target_ref", "final_commit_id", "commits"},
		},
		{
			"use":           "gs shell",
			"summary":       "browse server-side files",
			"flags":         []string{"--commit", "--slice"},
			"writes_stdout": true,
		},
		{
			"use":            "gs version",
			"summary":        "print CLI version and build information",
			"writes_stdout":  true,
			"machine_output": []string{"version", "commit", "build_date", "go_version", "dirty"},
		},
		{
			"use":           "gs schema",
			"summary":       "print this machine-readable CLI schema",
			"writes_stdout": true,
		},
		{
			"use":           "gs completion <shell>",
			"summary":       "print a shell completion script (bash, zsh, fish, powershell); changeset and slice arguments complete from the server",
			"args":          []string{"shell"},
			"writes_stdout": true,
		},
		{
			"use":            "gs help <topic>",
			"summary":        "show help for a command or CLI topic",
			"args":           []string{"command", "topic"},
			"writes_stdout":  true,
			"machine_output": []string{},
		},
	}
}
