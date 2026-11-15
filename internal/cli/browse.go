package cli

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// webAppRoutePrefixes are the first path segments of the web app's routes
// (web/src/routes/router.tsx). Browse targets starting with one are passed
// through as pages; anything else is treated as a slice ref or source path.
var webAppRoutePrefixes = map[string]bool{
	"slices": true, "cs": true, "changesets": true, "conversations": true,
	"doc": true, "blogs": true, "claims": true, "login": true, "cli-login": true,
}

// browseTarget is a resolved gs browse target: either a raw web page route, or
// a slice (plus optional absolute path) that maps to the slice page.
type browseTarget struct {
	route   string
	account string
	slice   string
	path    string
}

func (t browseTarget) webRoute() string {
	if t.account == "" {
		return t.route
	}
	route := "slices/" + url.PathEscape(t.account) + "/" + url.PathEscape(t.slice)
	if t.path != "" {
		route += "?path=" + url.QueryEscape(t.path)
	}
	return route
}

func (r Runner) runBrowse(ctx context.Context, opts commandOptions, webURL, rawTarget, rawPath string, printOnly bool) error {
	target, err := r.resolveBrowseTarget(ctx, rawTarget, rawPath)
	if err != nil {
		return err
	}
	link, err := webRouteURL(webURL, target.webRoute())
	if err != nil {
		return err
	}
	if !opts.Quiet {
		for _, note := range r.browseNotes(ctx, target) {
			fmt.Fprintln(r.stderr(), note)
		}
	}
	if printOnly {
		fmt.Fprintln(r.Stdout, link)
		return nil
	}
	if err := openBrowserURL(link); err != nil {
		hint := "Run gs browse --print"
		if strings.TrimSpace(rawTarget) != "" {
			hint += " " + rawTarget
		}
		hint += " to print the URL."
		return userError("browser_open_failed", "could not open browser: "+err.Error(), hint)
	}
	if opts.Quiet {
		return nil
	}
	fmt.Fprintf(r.stderr(), "opened %s\n", link)
	return nil
}

// browseChangesetHandle asks the server for a changeset and returns its
// shareable id. When the server cannot be asked, the id is used as typed.
func (r Runner) browseChangesetHandle(ctx context.Context, id string) (string, error) {
	cfg, err := r.readUserConfig()
	if err != nil {
		fmt.Fprintf(r.stderr(), "note: not signed in, so changeset %s was not checked.\n", id)
		return id, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cs, err := r.getChangeset(lookupCtx, cfg, id)
	switch grpcstatus.Code(err) {
	case codes.OK:
		handle := firstNonEmpty(displayChangesetID(cs), cs.Id)
		if _, commitShaped := storage.CommitIDLookupPrefix(id); commitShaped && !sameChangesetSelector(id, cs.Id) {
			fmt.Fprintf(r.stderr(), "%s is the commit changeset %s landed as; linking the changeset.\n", id, handle)
		}
		return handle, nil
	case codes.NotFound:
		return "", userError("changeset_not_found", "no changeset or landed commit "+id, "Check the id with gs cs list; gs prints changesets as 10 hex characters.")
	case codes.PermissionDenied, codes.Unauthenticated:
		return "", userError("changeset_not_visible", "changeset "+id+" is in a slice you cannot read", "Sign in with gs auth login, or ask the slice's owner for access.")
	default:
		fmt.Fprintf(r.stderr(), "warning: could not check changeset %s: %v\n", id, err)
		return id, nil
	}
}

// resolveBrowseTarget maps what the user typed to a real web app location.
func (r Runner) resolveBrowseTarget(ctx context.Context, rawTarget, rawPath string) (browseTarget, error) {
	target := strings.TrimSpace(rawTarget)
	pathArg := strings.TrimSpace(rawPath)
	if strings.Contains(target, "://") {
		return browseTarget{}, userError("invalid_args", "gs browse takes a slice, path, or page, not a full URL", "Pass account:slice, /account/path, or a page such as cs/<id>.")
	}

	// account:slice [--path p]
	if strings.Contains(target, ":") {
		ref, err := parseSliceRef(target)
		if err != nil {
			return browseTarget{}, err
		}
		out := browseTarget{account: ref.Account, slice: ref.Slice}
		if pathArg != "" {
			abs, err := r.browseAbsolutePath(pathArg)
			if err != nil {
				return browseTarget{}, err
			}
			out.path = abs
		}
		return out, nil
	}

	// No target: --path alone, the workspace slice, or the web home page.
	if target == "" {
		if pathArg != "" {
			return r.browseTargetForPath(ctx, pathArg)
		}
		if ws, err := r.readWorkspaceConfig(); err == nil && ws.Account != "" && ws.Slice != "" {
			return browseTarget{account: ws.Account, slice: ws.Slice}, nil
		}
		return browseTarget{route: ""}, nil
	}
	if pathArg != "" {
		return browseTarget{}, userError("invalid_args", "--path needs a slice target", "Use gs browse account:slice --path /account/path, or pass the path as the target.")
	}

	// A web app page, as-is.
	first := strings.SplitN(strings.TrimLeft(strings.SplitN(target, "?", 2)[0], "/"), "/", 2)[0]
	if webAppRoutePrefixes[first] {
		route := strings.TrimLeft(target, "/")
		parts := strings.Split(strings.SplitN(route, "?", 2)[0], "/")
		if parts[0] == "slices" && len(parts) >= 3 && parts[1] != "" && parts[2] != "" && parts[2] != "new" {
			return browseTarget{route: route, account: parts[1], slice: parts[2], path: browseRoutePathParam(route)}, nil
		}
		if parts[0] == "cs" && len(parts) == 2 && parts[1] != "" {
			// Check the changeset exists before handing out its link, and use
			// its own id (a landed commit's id also finds it).
			handle, err := r.browseChangesetHandle(ctx, parts[1])
			if err != nil {
				return browseTarget{}, err
			}
			query := ""
			if i := strings.Index(route, "?"); i >= 0 {
				query = route[i:]
			}
			return browseTarget{route: "cs/" + handle + query}, nil
		}
		return browseTarget{route: route}, nil
	}

	// Otherwise a source path: absolute (/account/...) or workspace-relative.
	return r.browseTargetForPath(ctx, target)
}

func browseRoutePathParam(route string) string {
	parts := strings.SplitN(route, "?", 2)
	if len(parts) < 2 {
		return ""
	}
	values, err := url.ParseQuery(parts[1])
	if err != nil {
		return ""
	}
	return values.Get("path")
}

// browseAbsolutePath turns an absolute (/account/...) or workspace-relative
// path into a canonical global path.
func (r Runner) browseAbsolutePath(p string) (string, error) {
	if strings.HasPrefix(p, "/") {
		canonical, err := paths.CanonicalPrefix(p)
		if err != nil {
			return "", userError("invalid_path", err.Error(), "Pass a path like /account/dir/file.go.")
		}
		return canonical, nil
	}
	root, err := r.workspaceRoot()
	if err != nil {
		return "", userError("invalid_path", fmt.Sprintf("%q is not a slice (account:slice), a source path (/account/...), or a web page", p),
			"Relative paths only work inside a gs workspace. Examples: gs browse heibot:home, gs browse /heibot/pricing, gs browse cs/<id>.")
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.cwd(), p)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", userError("invalid_path", fmt.Sprintf("%q is outside the current workspace", p), "Pass an absolute source path such as /account/dir instead.")
	}
	canonical, err := paths.CanonicalPrefix("/" + filepath.ToSlash(rel))
	if err != nil {
		return "", userError("invalid_path", err.Error(), "Pass a path inside the workspace's account directory.")
	}
	return canonical, nil
}

// browseTargetForPath picks the slice to show a path in: the current
// workspace's slice if it includes the path, else the most specific slice of
// the path's account that includes it, else that account's home slice (which
// covers the whole account).
func (r Runner) browseTargetForPath(ctx context.Context, p string) (browseTarget, error) {
	abs, err := r.browseAbsolutePath(p)
	if err != nil {
		return browseTarget{}, err
	}
	account := strings.SplitN(strings.TrimPrefix(abs, "/"), "/", 2)[0]
	if ws, err := r.readWorkspaceConfig(); err == nil && ws.Account != "" && paths.InAnyPrefix(ws.IncludedPaths, abs) {
		return browseTarget{account: ws.Account, slice: ws.Slice, path: abs}, nil
	}
	out := browseTarget{account: account, slice: "home", path: abs}
	cfg, err := r.readUserConfig()
	if err != nil {
		return out, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := dial(lookupCtx, cfg.ServerAddr)
	if err != nil {
		return out, nil
	}
	defer conn.Close()
	slices, err := listAllSlices(authContext(lookupCtx, cfg), corev1.NewSliceServiceClient(conn), account)
	if err != nil {
		return out, nil
	}
	best := -1
	for _, slice := range slices {
		for _, prefix := range slice.GetDefinition().GetIncludedPaths() {
			if paths.Contains(prefix, abs) && len(strings.TrimRight(prefix, "/")) > best {
				best = len(strings.TrimRight(prefix, "/"))
				out.slice = slice.GetRef().GetSlice()
			}
		}
	}
	return out, nil
}

// browseNotes checks a slice link before it is shared. Every note goes to
// stderr so stdout stays just the URL. Checks are best effort (5s).
func (r Runner) browseNotes(ctx context.Context, t browseTarget) []string {
	if t.account == "" {
		return nil
	}
	name := t.account + "/" + t.slice
	cfg, err := r.readUserConfig()
	if err != nil {
		return []string{fmt.Sprintf("note: not signed in, so %s was not checked. If it is private, the link only works for signed-in members of %s.", name, t.account)}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := dial(lookupCtx, cfg.ServerAddr)
	if err != nil {
		return []string{fmt.Sprintf("note: could not reach %s to check %s: %v", cfg.ServerAddr, name, err)}
	}
	defer conn.Close()
	authCtx := authContext(lookupCtx, cfg)
	slice, err := corev1.NewSliceServiceClient(conn).ResolveSlice(authCtx, &corev1.ResolveSliceRequest{
		Ref: &corev1.SliceRef{Account: t.account, Slice: t.slice},
	})
	if err != nil {
		switch grpcstatus.Code(err) {
		case codes.NotFound, codes.PermissionDenied, codes.Unauthenticated:
			return []string{fmt.Sprintf("warning: slice %s does not exist or is not visible to you; the link will not work. Check the name with gs slice list %s.", name, t.account)}
		}
		return []string{fmt.Sprintf("note: could not check %s: %v", name, err)}
	}
	var notes []string
	if t.path != "" {
		if ref, err := corev1.NewRepositoryServiceClient(conn).GetRef(authCtx, &corev1.GetRefRequest{RefName: storage.DefaultTargetRef}); err == nil && ref.GetCommitId() != "" {
			_, err := corev1.NewRepositoryServiceClient(conn).ResolvePath(authCtx, &corev1.ResolvePathRequest{
				CommitId: ref.GetCommitId(),
				Path:     t.path,
				Slice:    &corev1.SliceRef{Account: t.account, Slice: t.slice},
			})
			if grpcstatus.Code(err) == codes.NotFound {
				notes = append(notes, fmt.Sprintf("warning: %s does not exist in %s at the latest published commit (unsubmitted or still-publishing changes are not visible yet).", t.path, name))
			}
		}
	}
	if slice.GetDefinition().GetVisibility() == "private" {
		notes = append(notes, fmt.Sprintf("note: %s is private: the link only works for signed-in members of %s; others see a sign-in prompt.\n"+
			"      To share it with anyone: gs slice update %s:%s --visibility public", name, t.account, t.account, t.slice))
	}
	return notes
}

// webResourceURL builds a best-effort link into the web app for a created or
// updated resource, using GS_WEB_URL / the default web app. Returns "" if it
// cannot be built so callers can simply skip printing a link.
func webResourceURL(route string) string {
	link, err := webRouteURL(defaultWebURL(), route)
	if err != nil {
		return ""
	}
	return link
}

func webRouteURL(webURL, route string) (string, error) {
	webURL = strings.TrimSpace(webURL)
	if webURL == "" {
		webURL = defaultWebURL()
	}
	if !strings.Contains(webURL, "://") {
		webURL = "http://" + webURL
	}
	parsed, err := url.Parse(webURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", userError("invalid_web_url", "web-url must use http or https", "Pass --web-url http://127.0.0.1:8082.")
	}
	route = strings.TrimSpace(route)
	if route == "" {
		if parsed.Path == "" {
			parsed.Path = "/"
		}
		return parsed.String(), nil
	}
	routePath, routeQuery, hasQuery := strings.Cut(route, "?")
	routePath = strings.TrimSpace(routePath)
	basePath := strings.TrimRight(parsed.Path, "/")
	if routePath == "" || routePath == "/" {
		if basePath == "" {
			parsed.Path = "/"
		} else {
			parsed.Path = basePath + "/"
		}
	} else {
		if !strings.HasPrefix(routePath, "/") {
			routePath = "/" + routePath
		}
		cleaned := path.Clean(routePath)
		if strings.HasSuffix(routePath, "/") && cleaned != "/" {
			cleaned += "/"
		}
		parsed.Path = path.Join(basePath, cleaned)
	}
	if hasQuery {
		parsed.RawQuery = routeQuery
	}
	return parsed.String(), nil
}

func openBrowserURL(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}
