package checkexec

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

const (
	containerWorkspaceSrcPath = "/.gitslice-workspace-src"
	// defaultStageSize caps the in-memory /workspace: the staged tree plus
	// whatever the check writes there (build outputs). Caches live in their
	// own mounts and don't count.
	defaultStageSize = "4g"
	// stageFailedExit is the exit status when staging fails, so the check
	// never runs on a partial tree.
	stageFailedExit = 125
)

// stageWorkspaceScript is the container command when /workspace is staged.
// $1 is the check's working directory and $2 its command.
func stageWorkspaceScript() string {
	return stageScript(containerWorkspaceSrcPath, containerWorkspacePath)
}

// stageScript copies src's entries into dst, skipping the workspace's .gs
// metadata folder, then runs the check in its working directory. Any failed
// copy (an unreadable file, a full /workspace) stops it with
// stageFailedExit before the check starts; the loop alone would report only
// the last copy's status.
func stageScript(src, dst string) string {
	return fmt.Sprintf(`for entry in %[1]s/* %[1]s/.[!.]* %[1]s/..?*; do `+
		`[ -e "$entry" ] || [ -L "$entry" ] || continue; `+
		`[ "${entry##*/}" = .gs ] && continue; `+
		`cp -RPp "$entry" %[2]s/ || { echo "gitslice: could not copy the workspace into %[2]s (full? its size is GITSLICE_CHECK_STAGE_SIZE, now %[3]s); the check did not run" >&2; exit %[4]d; }; `+
		`done; cd "$1" || exit %[4]d; exec sh -c "$2"`,
		src, dst, stageSize(), stageFailedExit)
}

var stageSizePattern = regexp.MustCompile(`^[1-9][0-9]*[kmg]?$`)

// stageSize is the size cap of the staged /workspace (GITSLICE_CHECK_STAGE_SIZE,
// a tmpfs size such as 8g or 512m; default 4g). Anything else is ignored.
func stageSize() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GITSLICE_CHECK_STAGE_SIZE")))
	if !stageSizePattern.MatchString(v) {
		return defaultStageSize
	}
	return v
}

// workspaceStageTmpfs returns the --tmpfs mount spec for staging /workspace
// inside the container's native filesystem when host bind mounts traverse a VM
// file-sharing layer (VirtioFS on macOS Docker Desktop, where Node 22/libuv's
// recursive fs.cpSync leaves copied directory trees at mode 0200 and breaks
// subsequent Vite/Nitro builds with EACCES, and where host workspace runs
// should not write container symlinks or build outputs back into the host tree).
// The tmpfs is capped at stageSize, since it is held in memory.
// An empty string means bind-mount root directly at /workspace.
func workspaceStageTmpfs(rt RuntimeInfo, cachePaths []string) string {
	if !shouldStageWorkspace(cachePaths) {
		return ""
	}
	size := "size=" + stageSize()
	if !rt.Rootless && runtime.GOOS != "windows" {
		if uid, gid := os.Getuid(), os.Getgid(); uid > 0 {
			return fmt.Sprintf("%s:exec,%s,uid=%d,gid=%d,mode=0700", containerWorkspacePath, size, uid, gid)
		}
	}
	return containerWorkspacePath + ":exec," + size + ",mode=1777"
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
