package store

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// migrateEntryHashes normalises all persisted entry hashes to the current
// EntryHashLen format before state logs are folded. It rewrites both entry
// NDJSON files and read/starred logs, so the data dir never mixes legacy
// 20-char entry hashes with current 16-char hashes after a successful Open.
//
// It also collapses two classes of duplicate: (1) legacy unmasked hashes
// vs their high-bit-masked re-poll, and (2) volatile-pubDate guids whose
// id drifted between polls (see NormalizeGUID). For (2) the entry hash is
// recomputed from (normalised guid, link); the old→new remap is carried
// into the state-log rewrite so read/starred state follows the entry.
func migrateEntryHashes(dir string) error {
	remap := map[string]string{} // canonicalised old hash -> recomputed hash
	if err := migrateEntryFiles(filepath.Join(dir, "entries"), remap); err != nil {
		return err
	}
	for _, name := range []string{"read.log", "starred.log"} {
		if err := migrateStateLog(filepath.Join(dir, name), remap); err != nil {
			return err
		}
	}
	return nil
}

func migrateEntryFiles(root string, remap map[string]string) error {
	seen := map[string]string{} // canonical -> original, for collision audit
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".ndjson") {
			return nil
		}
		return migrateEntryFile(path, seen, remap)
	}); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func migrateEntryFile(path string, seen, remap map[string]string) error {
	var entries []Entry
	changed := false
	emitted := make(map[string]bool) // canonical hashes already kept in THIS file
	if err := scanEntries(path, func(e Entry) error {
		old := e.Hash
		canon := StoreEntryHash(old)
		// Recompute identity for volatile-pubDate guids: the same article
		// collapses to one stable hash even though its guid (and thus its
		// stored hash) drifted between polls. EntryHash normalises the
		// guid and masks the high bit, so the result is already canonical.
		if e.GUID != "" && NormalizeGUID(e.GUID) != e.GUID {
			if rec := EntryHash(e.GUID, e.Link); rec != canon {
				remap[canon] = rec
				canon = rec
			}
		}
		if prev, ok := seen[canon]; ok && prev != old && len(prev) > EntryHashLen && len(old) > EntryHashLen {
			return fmt.Errorf("entry hash collision migrating %s: %s and %s both map to %s", path, prev, old, canon)
		}
		seen[canon] = old
		if canon != old {
			e.Hash = canon
			changed = true
		}
		// Drop intra-file duplicates: a legacy unmasked hash and its
		// masked re-poll (or a volatile-pubDate twin) collapse to the
		// same canonical id, so the same article can sit in the file
		// twice. Keep the first, prune the rest (and rewrite the file to
		// make the prune durable).
		if emitted[canon] {
			changed = true
			return nil
		}
		emitted[canon] = true
		entries = append(entries, e)
		return nil
	}); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	var b strings.Builder
	for _, e := range entries {
		line, err := jsonMarshal(e)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return atomicWriteFile(path, []byte(b.String()))
}

func migrateStateLog(path string, remap map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	changed := false
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, " ", 3)
		if len(parts) == 3 {
			canon := StoreEntryHash(parts[2])
			if rec, ok := remap[canon]; ok {
				canon = rec
			}
			if canon != parts[2] {
				changed = true
				line = parts[0] + " " + parts[1] + " " + canon
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return atomicWriteFile(path, []byte(b.String()))
}
