package checkexec

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	containerWorkspaceSrcPath = "/.gitslice-workspace-src"
	stageWorkspaceScript      = `for entry in /.gitslice-workspace-src/* /.gitslice-workspace-src/.[!.]* /.gitslice-workspace-src/..?*; do [ -e "$entry" ] || [ -L "$entry" ] || continue; cp -RPp "$entry" /workspace/; done && cd "$1" && exec sh -c "$2"`
)

// workspaceStageTmpfs returns the --tmpfs mount spec for staging /workspace
// inside the container's native filesystem when host bind mounts traverse a VM
// file-sharing layer (VirtioFS on macOS Docker Desktop, where Node 22/libuv's
// recursive fs.cpSync leaves copied directory trees at mode 0200 and breaks
// subsequent Vite/Nitro builds with EACCES, and where host workspace runs
// should not write container symlinks or build outputs back into the host tree).
// An empty string means bind-mount root directly at /workspace.
func workspaceStageTmpfs(rt RuntimeInfo, cachePaths []string) string {
	if !shouldStageWorkspace(cachePaths) {
		return ""
	}
	if !rt.Rootless && runtime.GOOS != "windows" {
		if uid, gid := os.Getuid(), os.Getgid(); uid > 0 {
			return fmt.Sprintf("%s:exec,uid=%d,gid=%d,mode=0700", containerWorkspacePath, uid, gid)
		}
	}
	return containerWorkspacePath + ":exec,mode=1777"
}

func shouldStageWorkspace(cachePaths []string) bool {
	for _, p := range cachePaths {
		if p == containerWorkspacePath || strings.HasPrefix(p, containerWorkspacePath+"/") {
			return false
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GITSLICE_CHECK_STAGE_WORKSPACE"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		return runtime.GOOS == "darwin"
	}
}
