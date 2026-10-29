package triggers

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	gspaths "gitslice.io/gitslice/internal/paths"
)

// ErrNotFound is what a TreeReader returns for a missing file.
var ErrNotFound = errors.New("not found")

// TreeReader reads a file at a canonical path (/account/dir/file) of a tree.
type TreeReader interface {
	ReadFile(ctx context.Context, rootTreeID, path string) ([]byte, error)
}

// Change is one changed file, at canonical paths.
type Change struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	OldPath string `json:"old_path,omitempty"`
}

// Event is what happened, as far as choosing triggers goes.
type Event struct {
	Kind string
	// Account owns every folder involved: /<Account>/...
	Account string
	Ref     string
	// Changes are the files the event is about. Their folders and the
	// folders above them are searched for triggers.
	Changes []Change
	// Folders are searched, with the folders above them, when the event is
	// not about files (a tag: the tagging slice's included paths).
	Folders []string
	// Only, when set, runs just this trigger id (a manual run).
	Only string
}

// Match is a trigger the event sets off.
type Match struct {
	// ID is "<dir>/<name>", the dir relative to the account root, or
	// "<name>" at the root, as check ids are.
	ID   string
	Name string
	// Folder is the canonical folder that holds the .gitslice folder.
	Folder  string
	Trigger Trigger
	// Changes are the event's changes this trigger matched.
	Changes []Change
}

// Problem is a triggers file on the event's path that cannot be used.
type Problem struct {
	Folder  string
	File    string
	Message string
}

// Resolve reads the triggers files on the event's folders from the tree and
// returns the triggers it sets off, sorted by id, and the files that do not
// parse.
func Resolve(ctx context.Context, tree TreeReader, rootTreeID string, ev Event) ([]Match, []Problem, error) {
	root := "/" + strings.Trim(ev.Account, "/")
	var folders []string
	seen := map[string]bool{}
	add := func(p string) {
		for _, folder := range folderSpine(root, p) {
			if !seen[folder] {
				seen[folder] = true
				folders = append(folders, folder)
			}
		}
	}
	if ev.Only != "" {
		dir, _ := splitID(ev.Only)
		add(path.Join(root, dir) + "/x")
	} else {
		for _, c := range ev.Changes {
			add(c.Path)
			if c.OldPath != "" {
				add(c.OldPath)
			}
		}
		for _, f := range ev.Folders {
			add(strings.TrimRight(f, "/") + "/x")
		}
	}
	sort.Strings(folders)

	var matches []Match
	var problems []Problem
	for _, folder := range folders {
		file := folder + "/" + FileName
		data, err := tree.ReadFile(ctx, rootTreeID, file)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", file, err)
		}
		parsed, err := Parse(data)
		if err != nil {
			problems = append(problems, Problem{Folder: folder, File: file, Message: err.Error()})
			continue
		}
		for _, name := range sortedNames(parsed.Triggers) {
			t := parsed.Triggers[name]
			id := ID(root, folder, name)
			if ev.Only != "" {
				if id == ev.Only {
					matches = append(matches, Match{ID: id, Name: name, Folder: folder, Trigger: t})
				}
				continue
			}
			changes, ok, err := matchTrigger(root, folder, t, ev)
			if err != nil {
				problems = append(problems, Problem{Folder: folder, File: file, Message: fmt.Sprintf("trigger %s: %v", name, err)})
				continue
			}
			if ok {
				matches = append(matches, Match{ID: id, Name: name, Folder: folder, Trigger: t, Changes: changes})
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches, problems, nil
}

// matchTrigger says whether a trigger fires for the event, and for which of
// its changes.
func matchTrigger(root, folder string, t Trigger, ev Event) ([]Change, bool, error) {
	if !contains(t.On, ev.Kind) {
		return nil, false, nil
	}
	if len(t.Refs) > 0 && !refMatches(t.Refs, ev.Ref) {
		return nil, false, nil
	}
	if len(ev.Changes) == 0 {
		// A tag is not about files: it fires in the folders the event names.
		// A changeset event without files (created before its first
		// patchset) has nothing to run on.
		return nil, ev.Kind == EventTagCreated, nil
	}
	var matched []Change
	for _, c := range ev.Changes {
		if len(t.Changes) > 0 && !contains(t.Changes, c.Kind) {
			continue
		}
		ok, err := changeMatches(root, folder, t.Paths, c)
		if err != nil {
			return nil, false, err
		}
		if ok {
			matched = append(matched, c)
		}
	}
	return matched, len(matched) > 0, nil
}

// changeMatches applies a trigger's paths globs to a change under its
// folder: folder-relative globs, "/"-globs relative to the account root, and
// "!" to exclude. A move matches by its new or its old path.
func changeMatches(root, folder string, patterns []string, c Change) (bool, error) {
	for _, p := range []string{c.Path, c.OldPath} {
		if p == "" || !gspaths.Contains(folder, p) {
			continue
		}
		ok, err := globsMatch(root, folder, patterns, p)
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

func globsMatch(root, folder string, patterns []string, p string) (bool, error) {
	included := true
	for _, pattern := range patterns {
		if !strings.HasPrefix(pattern, "!") {
			included = false
			break
		}
	}
	for _, pattern := range patterns {
		negate := strings.HasPrefix(pattern, "!")
		glob := strings.TrimPrefix(pattern, "!")
		var target string
		if strings.HasPrefix(glob, "/") {
			glob = strings.TrimPrefix(glob, "/")
			target = strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		} else {
			target = strings.TrimPrefix(strings.TrimPrefix(p, folder), "/")
		}
		ok, err := doublestar.Match(glob, target)
		if err != nil {
			return false, fmt.Errorf("paths glob %q: %w", pattern, err)
		}
		if ok && negate {
			return false, nil
		}
		if ok {
			included = true
		}
	}
	return included, nil
}

// refMatches compares refs, where "main" stands for refs/global/main.
func refMatches(refs []string, ref string) bool {
	for _, r := range refs {
		if r == ref || "refs/global/"+r == ref || "refs/heads/"+r == ref {
			return true
		}
	}
	return false
}

// folderSpine lists the folders from root down to the folder holding p.
func folderSpine(root, p string) []string {
	p = path.Clean("/" + strings.TrimSpace(p))
	if !gspaths.Contains(root, p) {
		return nil
	}
	out := []string{root}
	rel := strings.TrimPrefix(strings.TrimPrefix(path.Dir(p), root), "/")
	if rel == "" || path.Dir(p) == root {
		return out
	}
	current := root
	for _, segment := range strings.Split(rel, "/") {
		current += "/" + segment
		out = append(out, current)
	}
	return out
}

// ID is a trigger's id: its folder relative to the account root, then its
// name; just the name at the root.
func ID(root, folder, name string) string {
	dir := strings.TrimPrefix(strings.TrimPrefix(folder, root), "/")
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// splitID splits a trigger or check id into its folder (relative to the
// account root) and name.
func splitID(id string) (string, string) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

// FolderOf returns the canonical folder of a trigger or check id in an
// account.
func FolderOf(account, id string) string {
	dir, _ := splitID(id)
	root := "/" + strings.Trim(account, "/")
	if dir == "" {
		return root
	}
	return root + "/" + dir
}

// CheckID is the id of a check a trigger reports: its folder, then the
// check's name, as for checks.yaml.
func CheckID(triggerID, checkName string) string {
	dir, _ := splitID(triggerID)
	if dir == "" {
		return checkName
	}
	return dir + "/" + checkName
}

func sortedNames(m map[string]Trigger) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// List returns every trigger defined in folder or a folder above it, and the
// triggers files there that do not parse.
func List(ctx context.Context, tree TreeReader, rootTreeID, account, folder string) ([]Match, []Problem, error) {
	root := "/" + strings.Trim(account, "/")
	var matches []Match
	var problems []Problem
	for _, f := range folderSpine(root, strings.TrimRight(folder, "/")+"/x") {
		file := f + "/" + FileName
		data, err := tree.ReadFile(ctx, rootTreeID, file)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", file, err)
		}
		parsed, err := Parse(data)
		if err != nil {
			problems = append(problems, Problem{Folder: f, File: file, Message: err.Error()})
			continue
		}
		for _, name := range sortedNames(parsed.Triggers) {
			matches = append(matches, Match{ID: ID(root, f, name), Name: name, Folder: f, Trigger: parsed.Triggers[name]})
		}
	}
	return matches, problems, nil
}

// changesetEvents are the events that happen while a changeset is open, so
// a trigger listening for one of them can report a check before submit.
var changesetEvents = []string{EventChangesetCreated, EventChangesetUpdated, EventChangesetApproved, EventCheckRunCompleted}

// CheckSkipped reports whether a check id is reported by a trigger
// (declared in the checks of a trigger in the check's folder) that no event
// of an open changeset with these changes would set off. Such a required
// check counts as passed, as a checks.yaml check whose paths do not match
// is skipped. declared is false when no trigger reports the check.
func CheckSkipped(ctx context.Context, tree TreeReader, rootTreeID, account, ref, checkID string, changes []Change) (skipped, declared bool, err error) {
	root := "/" + strings.Trim(account, "/")
	folder := FolderOf(account, checkID)
	_, checkName := splitID(checkID)
	data, err := tree.ReadFile(ctx, rootTreeID, folder+"/"+FileName)
	if errors.Is(err, ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	parsed, err := Parse(data)
	if err != nil {
		return false, false, nil
	}
	for _, name := range sortedNames(parsed.Triggers) {
		t := parsed.Triggers[name]
		if !contains(t.Checks, checkName) {
			continue
		}
		declared = true
		for _, kind := range changesetEvents {
			if !contains(t.On, kind) {
				continue
			}
			_, fires, err := matchTrigger(root, folder, t, Event{Kind: kind, Account: account, Ref: ref, Changes: changes})
			if err != nil {
				return false, true, err
			}
			if fires {
				return false, true, nil
			}
		}
	}
	return declared, declared, nil
}
