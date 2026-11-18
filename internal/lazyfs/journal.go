package lazyfs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func validateRecordSet(set map[string]*localEntry) error {
	for p, entry := range set {
		clean, err := cleanWritablePath(p)
		if err != nil {
			return err
		}
		if clean == "/" || clean != p {
			return fmt.Errorf("lazyfs: invalid journal path %q: %w", p, fs.ErrInvalid)
		}
		if entry != nil && entry.Kind == entryFile && !isValidLocalName(entry.Local) {
			return fmt.Errorf("lazyfs: invalid local content name %q for %s: %w", entry.Local, p, fs.ErrInvalid)
		}
	}
	return nil
}

// replay applies the journal and truncates it after its last complete record.
func (f *FS) replay() (string, int, error) {
	jInfo, err := f.dirRoot.Lstat(journalName)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if !jInfo.Mode().IsRegular() {
		return "", 0, fmt.Errorf("lazyfs: %s/%s is not a regular file: %w", f.dir, journalName, fs.ErrInvalid)
	}
	file, err := f.dirRoot.OpenFile(journalName, os.O_RDWR, 0o600)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var good int64
	base := ""
	records := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break // a record without its newline was cut short
		}
		var rec record
		if json.Unmarshal(line, &rec) != nil {
			break
		}
		if err := validateRecordSet(rec.Set); err != nil {
			return "", 0, err
		}
		if rec.Snapshot {
			base = rec.Base
			f.reset(rec.Set)
		} else {
			for p, entry := range rec.Set {
				f.setLocked(p, entry)
			}
		}
		records++
		good += int64(len(line))
	}
	if err := file.Truncate(good); err != nil {
		return "", 0, err
	}
	return base, records, nil
}

// commitLocked journals a batch of changes, then applies it.
func (f *FS) commitLocked(set map[string]*localEntry) error {
	if len(set) == 0 {
		return nil
	}
	line, err := json.Marshal(record{Set: set})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if err := f.at("journal-half"); err != nil {
		_, _ = f.journal.Write(line[:len(line)/2])
		return err
	}
	if _, err := f.journal.Write(line); err != nil {
		return err
	}
	if !f.opts.NoSync {
		if err := f.journal.Sync(); err != nil {
			return err
		}
	}
	if err := f.at("journal-written"); err != nil {
		return err
	}
	for p, entry := range set {
		f.setLocked(p, entry)
	}
	f.records++
	return nil
}

// writeSnapshot replaces the journal, atomically, with one record holding the
// whole layer over base.
func (f *FS) writeSnapshot(base string) error {
	line, err := json.Marshal(record{Snapshot: true, Base: base, Set: f.entries})
	if err != nil {
		return err
	}
	tmpName := journalName + ".tmp"
	_ = f.dirRoot.Remove(tmpName)
	tmpFile, err := f.dirRoot.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := tmpFile.Write(append(line, '\n')); err != nil {
		_ = tmpFile.Close()
		_ = f.dirRoot.Remove(tmpName)
		return err
	}
	if !f.opts.NoSync {
		if err := tmpFile.Sync(); err != nil {
			_ = tmpFile.Close()
			_ = f.dirRoot.Remove(tmpName)
			return err
		}
	}
	if err := tmpFile.Close(); err != nil {
		_ = f.dirRoot.Remove(tmpName)
		return err
	}
	if f.journal != nil {
		_ = f.journal.Close()
		f.journal = nil
	}
	if err := verifyRealDir(f.dir); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(f.dir, tmpName), filepath.Join(f.dir, journalName)); err != nil {
		return err
	}
	if !f.opts.NoSync {
		if err := syncFile(f.dir); err != nil {
			return err
		}
	}
	f.records = 1
	f.journal, err = f.dirRoot.OpenFile(journalName, os.O_WRONLY|os.O_APPEND, 0o600)
	return err
}

func syncFile(p string) error {
	file, err := os.Open(p)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// collectGarbage deletes local content files no entry refers to: left by an
// operation a crash interrupted, or by one that replaced them.
func (f *FS) collectGarbage() error {
	used := map[string]bool{}
	for _, entry := range f.entries {
		if entry.Kind == entryFile {
			used[entry.Local] = true
		}
	}
	dirFile, err := f.filesRoot.Open(".")
	if err != nil {
		return err
	}
	defer dirFile.Close()
	names, err := dirFile.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !used[name.Name()] {
			_ = f.filesRoot.Remove(name.Name())
		}
	}
	return nil
}
