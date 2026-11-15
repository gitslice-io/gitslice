package gitcompat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Covers keep the expensive part of serving a clone (design/23_large_git_projection.md, "Covers"):
// writing every file's contents into the repository and compressing them.
// A lazily built projection holds commits and trees only, so a clone on a new
// instance used to read every file of the history from the object store and
// then have git compress them all for that one response: two minutes for the
// Gitslice slice (5,364 files), past Cloudflare's 100-second limit.
//
// Compact does it once: it writes every missing file, repacks the repository
// into one pack with deltas and a bitmap index, and mirrors that pack, with
// the head it is complete at. An instance that restores it has every file
// already and answers a clone by sending the pack almost as it is, as OpenAI's
// Pando does with its stored "covers". Landings add small packs on top; when
// there are more than coverMaxPacks, the next fetch asks for a new cover.
//
// Compaction is CPU work, and Cloud Run gives a container CPU only while it
// serves a request, so it runs in a request of its own: the server asks
// itself (Handler.serveCompact), never in the background, and never in the
// clone that needed it.

// coverMaxPacks is how many packs a repository may hold before a fetch asks
// for a new cover.
const coverMaxPacks = 8

// coverMaxBytes is the most a repository may hold on disk for a cover to be
// built: a container's disk is its memory, and a lazily built projection of a
// larger slice is meant to stay without its files.
var coverMaxBytes int64 = 512 << 20

// coverCooldown is how long after asking for a cover the same instance waits
// before asking again for that repository.
const coverCooldown = 10 * time.Minute

// CompactRequester starts a compaction of a slice's projection somewhere it
// gets CPU (a request to the server itself). It must not block.
type CompactRequester func(account, slice string)

// SetCompactRequester sets how fetches ask for covers; nil compacts in this
// process, in the fetch's handler (tests and local servers, which have CPU).
func (p *Projector) SetCompactRequester(r CompactRequester) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.compactRequester = r
}

// serveLock is held for reading while git serves the repository and for
// writing while a compaction replaces its packs.
func (p *Projector) serveLock(repoPath string) *sync.RWMutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.serveLocks == nil {
		p.serveLocks = map[string]*sync.RWMutex{}
	}
	lock := p.serveLocks[repoPath]
	if lock == nil {
		lock = &sync.RWMutex{}
		p.serveLocks[repoPath] = lock
	}
	return lock
}

// packNames lists the packs in the repository, sorted.
func packNames(repoPath string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(repoPath, "objects", "pack"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".pack"); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// noteFullFetch records that a fetch asked for whole files of the history
// (no filter), which is what a cover serves.
func (p *Projector) noteFullFetch(repoPath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fullFetches == nil {
		p.fullFetches = map[string]bool{}
	}
	p.fullFetches[repoPath] = true
}

func (p *Projector) fullFetchSeen(repoPath string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fullFetches[repoPath]
}

// dirBytes is the size of the files under dir.
func dirBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// lockedState reads the projection state under the repository's lock.
func (p *Projector) lockedState(repoPath string) *projectionState {
	lock := p.lockFor(repoPath)
	lock.Lock()
	defer lock.Unlock()
	return loadProjectionState(repoPath)
}

// needsCover reports whether the repository should be compacted: it has no
// cover yet, or too many packs on top of one.
func (p *Projector) needsCover(repoPath string) bool {
	// Only after a full clone or fetch (fullFetch): what partial clones and
	// lazy checkouts need is written as they ask, and a cover would write the
	// whole history.
	if !p.fullFetchSeen(repoPath) {
		return false
	}
	if size, err := dirBytes(filepath.Join(repoPath, "objects")); err != nil || size > coverMaxBytes {
		return false
	}
	state := loadProjectionState(repoPath)
	if state == nil || state.GitHead == "" {
		return false
	}
	if state.CoverHead == "" {
		return true
	}
	names, err := packNames(repoPath)
	return err == nil && len(names) > coverMaxPacks
}

// MaybeRequestCover asks for a compaction of the slice when its repository
// needs one, at most once per coverCooldown on this instance. A fetch calls it
// after its response is written; without a requester it compacts before it
// returns.
func (p *Projector) MaybeRequestCover(account, slice string) {
	repoPath := filepath.Join(p.cacheRoot, account, slice+".git")
	if !p.needsCover(repoPath) {
		return
	}
	p.mu.Lock()
	if p.coverAsked == nil {
		p.coverAsked = map[string]time.Time{}
	}
	if at, ok := p.coverAsked[repoPath]; ok && time.Since(at) < coverCooldown {
		p.mu.Unlock()
		return
	}
	p.coverAsked[repoPath] = time.Now()
	request := p.compactRequester
	p.mu.Unlock()
	if request != nil {
		request(account, slice)
		return
	}
	// In-process (tests and local servers, which have CPU), in the fetch's
	// handler after its response, so nothing runs on in the background.
	ctx, cancel := context.WithTimeout(context.Background(), compactTimeout)
	defer cancel()
	if err := p.Compact(ctx, account, slice); err != nil {
		slog.Warn("git cover failed", "repo", account+"/"+slice+".git", "error", err)
	}
}

// Compact builds the slice's cover: it brings the projection up to date,
// writes every file its history needs, repacks everything into one pack with
// deltas and a bitmap index, and mirrors it. It is for the server's own use:
// it reads private slices without a subject.
func (p *Projector) Compact(ctx context.Context, account, slice string) error {
	started := time.Now()
	repoPath, err := p.ensureProjected(ctx, account, slice)
	if err != nil {
		return err
	}
	return p.compactRepo(ctx, account, slice, repoPath, started)
}

// compactRepo is Compact on a projection that is up to date.
func (p *Projector) compactRepo(ctx context.Context, account, slice, repoPath string, started time.Time) error {
	// Fetches do not hydrate while this does; they keep being served from
	// what is there until the packs are replaced.
	hydrate := p.hydrateLock(repoPath)
	hydrate.Lock()
	defer hydrate.Unlock()
	state := p.lockedState(repoPath)
	if state == nil || state.GitHead == "" {
		return fmt.Errorf("%s/%s has no projected history", account, slice)
	}
	head := state.GitHead
	if state.CoverHead == head {
		return nil // another request covered it
	}
	missing, err := p.missingContents(ctx, repoPath, uploadRequest{fetch: true, wants: []string{head}}, true, p.completeAt(repoPath))
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		if err := p.hydrate(ctx, repoPath, missing); err != nil {
			return err
		}
	}
	// Landings may have moved the head meanwhile: the cover is complete at the
	// head it wrote the files for, and the next fetch asks for another.
	lock := p.lockFor(repoPath)
	lock.Lock()
	serve := p.serveLock(repoPath)
	serve.Lock()
	err = runGit(ctx, repoPath, []string{"GIT_DIR=" + repoPath},
		"-c", "pack.writeBitmapHashCache=true", "-c", "repack.writeBitmaps=true",
		"repack", "-a", "-d", "-b", "-q")
	var names []string
	if err == nil {
		names, err = packNames(repoPath)
	}
	if err == nil && len(names) != 1 {
		err = fmt.Errorf("repack left %d packs", len(names))
	}
	if err == nil {
		if state = loadProjectionState(repoPath); state == nil {
			err = errors.New("projection state went missing")
		}
	}
	if err == nil {
		state.HistoryPacks = names
		state.CompleteAt = head
		state.CoverHead = head
		err = writeProjectionState(repoPath, state)
	}
	serve.Unlock()
	if err != nil {
		lock.Unlock()
		return fmt.Errorf("repack: %w", err)
	}
	p.setComplete(repoPath, head)
	p.setPublished(repoPath, state.NativeHeadID)
	p.mirror.Publish(ctx, account, slice, repoPath, state)
	lock.Unlock()
	// This request holds the CPU until the cover is in the object store.
	for range 30 {
		if err = p.mirror.Settle(ctx, account, slice); err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("mirror the cover: %w", err)
	}
	size := int64(0)
	if info, statErr := os.Stat(filepath.Join(repoPath, "objects", "pack", names[0]+".pack")); statErr == nil {
		size = info.Size()
	}
	slog.Info("git cover built", "repo", account+"/"+slice+".git", "files_written", len(missing), "pack_bytes", size, "total_ms", time.Since(started).Milliseconds())
	return nil
}
