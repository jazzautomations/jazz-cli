package main

// gitindex.go: the file list of a work tree from .git/index (low-level study
// §5.2, item 7). The index is one sequential file with every tracked path in
// order; reading it replaces one open+getdents+close per directory of the
// name index's walk (on the reference machine's HDD a cold directory is a
// head seek). Pure Go, no `git` process.
//
// Untracked files and deletions are not in the index, so the listing is
// completed from the directories themselves. A directory is read for real
// when (a) it is the root (build outputs land there), (b) its mtime is not
// clearly older than the index file, or (c) its mtime is newer than the
// newest ctime git recorded for the tracked files directly in it (an entry
// was created, removed or renamed after the last tracked change there);
// every subdirectory the index does not know is walked like WalkDir.
// Known gap: an untracked file created in a subdirectory, after which a
// tracked file of that same directory was changed in place and git rewrote
// the index — then (b) and (c) both miss it. The persisted name index marks
// every directory not read here as unverified and reads it on its next
// build (nameindex_idx.go), so the gap lasts one build.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// giEntry is one path of the index.
type giEntry struct {
	Path    string
	Mode    uint32
	Size    uint32
	MtimeS  uint32
	CtimeNS int64 // ctime git recorded (unix ns)
}

const (
	giModeGitlink = 0o160000
	giModeDir     = 0o040000 // sparse-index directory entry
)

// giGitDir is the git dir of the work tree at root ("" = not a work-tree
// root): root/.git, or the gitdir a .git file points to (worktrees).
func giGitDir(root string) string {
	p := filepath.Join(root, ".git")
	fi, err := os.Lstat(p)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return p
	}
	if !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	d := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(d) {
		d = filepath.Join(root, d)
	}
	return d
}

var errGiFormat = errors.New("gitindex: unsupported or corrupt index")

// giParse decodes an index (versions 2, 3 and 4; SHA-1 or SHA-256 object
// ids). Stage-0 and conflict entries are kept once per path; skip-worktree
// entries (sparse checkout: not on disk) are dropped. A sparse index
// (directory entries) is refused: the caller walks instead.
func giParse(b []byte) ([]giEntry, error) {
	if len(b) < 12+20 || string(b[:4]) != "DIRC" {
		return nil, errGiFormat
	}
	ver := binary.BigEndian.Uint32(b[4:8])
	n := int(binary.BigEndian.Uint32(b[8:12]))
	if ver < 2 || ver > 4 || n < 0 || n > len(b)/40 {
		return nil, errGiFormat
	}
	for _, hash := range []int{20, 32} {
		if out, err := giParseEntries(b, ver, n, hash); err == nil {
			return out, nil
		}
	}
	return nil, errGiFormat
}

func giParseEntries(b []byte, ver uint32, n, hash int) ([]giEntry, error) {
	out := make([]giEntry, 0, n)
	off := 12
	prev := ""
	for i := 0; i < n; i++ {
		start := off
		if off+40+hash+2 > len(b) {
			return nil, errGiFormat
		}
		ctimeNS := int64(binary.BigEndian.Uint32(b[off:]))*1e9 + int64(binary.BigEndian.Uint32(b[off+4:]))
		mtimeS := binary.BigEndian.Uint32(b[off+8:])
		mode := binary.BigEndian.Uint32(b[off+24:])
		size := binary.BigEndian.Uint32(b[off+36:])
		off += 40 + hash
		flags := binary.BigEndian.Uint16(b[off:])
		off += 2
		skip := false
		if flags&0x4000 != 0 { // extended flags (v3+)
			if ver < 3 || off+2 > len(b) {
				return nil, errGiFormat
			}
			ext := binary.BigEndian.Uint16(b[off:])
			off += 2
			skip = ext&0x4000 != 0 // skip-worktree
		}
		var path string
		if ver == 4 {
			// prefix compression: strip N bytes of the previous path, add a
			// NUL-terminated suffix; no padding
			strip, k := giVarint(b[off:])
			if k == 0 || strip > len(prev) {
				return nil, errGiFormat
			}
			off += k
			z := bytes.IndexByte(b[off:], 0)
			if z < 0 {
				return nil, errGiFormat
			}
			path = prev[:len(prev)-strip] + string(b[off:off+z])
			off += z + 1
		} else {
			z := bytes.IndexByte(b[off:], 0)
			if z < 0 {
				return nil, errGiFormat
			}
			path = string(b[off : off+z])
			// entry padded with 1-8 NULs to a multiple of 8 bytes
			off = start + ((off + z - start + 8) &^ 7)
			if off > len(b) {
				return nil, errGiFormat
			}
		}
		if path == "" || path < prev && ver != 4 || strings.HasPrefix(path, "/") || strings.Contains(path, "//") {
			return nil, errGiFormat
		}
		if mode&0o170000 == giModeDir || strings.HasSuffix(path, "/") {
			return nil, errGiFormat // sparse index
		}
		if path != prev && !skip { // conflict stages repeat the path
			out = append(out, giEntry{Path: path, Mode: mode, Size: size, MtimeS: mtimeS, CtimeNS: ctimeNS})
		}
		prev = path
	}
	if len(b)-off < hash { // extensions + trailing checksum must remain
		return nil, errGiFormat
	}
	return out, nil
}

// giVarint is git's offset varint (varint.c); k = bytes read (0 = bad).
func giVarint(b []byte) (val, k int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	val = int(c & 127)
	k = 1
	for c&128 != 0 {
		if k >= len(b) || k > 8 {
			return 0, 0
		}
		c = b[k]
		k++
		val = ((val + 1) << 7) | int(c&127)
	}
	return val, k
}

// niSkipName: a directory the name index never enters (same rule as the walk).
func niSkipName(name string) bool {
	return rtSkipDir[name] || (strings.HasPrefix(name, ".") && name != ".github")
}

// niWalkLess orders rel paths like filepath.WalkDir visits them (names
// sorted within each directory, depth first): bytewise with '/' below
// every other byte.
func niWalkLess(a, b string) bool {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		ca, cb := a[i], b[i]
		if ca == cb {
			continue
		}
		if ca == '/' {
			ca = 0
		}
		if cb == '/' {
			cb = 0
		}
		return ca < cb
	}
	return len(a) < len(b)
}

// niDirState is one directory of a listing: rel path ("" = root), the mtime
// seen when it was read, and whether it was really read (false = taken from
// .git/index, unverified).
type niDirState struct {
	Rel   string
	ModNS int64
	Read  bool
}

// niListing is the file list of root in walk order plus the directories it
// came from.
type niListing struct {
	files []string
	dirs  []niDirState
	trunc bool
	git   bool // built from .git/index
}

// niListWalk is the reference listing: filepath.WalkDir with the index's
// skip rules, every directory read.
func niListWalk(root string) niListing {
	var l niListing
	niWalkInto(root, "", &l, niMaxFiles)
	return l
}

// niWalkInto walks rel (under root) appending to l.
func niWalkInto(root, rel string, l *niListing, limit int) {
	start := root
	if rel != "" {
		start = filepath.Join(root, filepath.FromSlash(rel))
	}
	_ = filepath.WalkDir(start, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && niSkipName(name) {
				return filepath.SkipDir
			}
			r := niRel(root, p)
			var mod int64
			if fi, err := d.Info(); err == nil {
				mod = fi.ModTime().UnixNano()
			}
			l.dirs = append(l.dirs, niDirState{Rel: r, ModNS: mod, Read: true})
			return nil
		}
		if len(l.files) >= limit {
			l.trunc = true
			return filepath.SkipAll
		}
		if r := niRel(root, p); r != "" {
			l.files = append(l.files, r)
		}
		return nil
	})
}

func niRel(root, p string) string {
	if p == root {
		return ""
	}
	r, err := filepath.Rel(root, p)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(r)
}

// giRacy: a directory changed less than this before the index was written
// may have changed after git read it.
const giRacy = 2 * time.Second

// niListGit builds the listing from the index of the work tree at root
// (ok=false: no usable index; the caller walks).
func niListGit(root string) (niListing, bool) {
	gd := giGitDir(root)
	if gd == "" {
		return niListing{}, false
	}
	ip := filepath.Join(gd, "index")
	ifi, err := os.Stat(ip)
	if err != nil || ifi.Size() > 256<<20 {
		return niListing{}, false
	}
	b, err := os.ReadFile(ip)
	if err != nil {
		return niListing{}, false
	}
	ents, err := giParse(b)
	if err != nil || len(ents) > niMaxFiles {
		return niListing{}, false // a huge repo keeps the walk's truncation order
	}
	idxMod := ifi.ModTime()
	// tracked files grouped by directory, minus the skipped trees
	byDir := map[string][]string{}
	newest := map[string]int64{} // newest recorded ctime of the tracked files of each dir
	gitlinks := map[string]bool{}
	byDir[""] = nil
	for _, e := range ents {
		if niPathSkipped(e.Path) {
			continue
		}
		dir := ""
		if j := strings.LastIndexByte(e.Path, '/'); j >= 0 {
			dir = e.Path[:j]
		}
		if e.Mode&0o170000 == giModeGitlink {
			gitlinks[e.Path] = true // a submodule: walked like any directory
		} else {
			byDir[dir] = append(byDir[dir], e.Path)
			newest[dir] = max(newest[dir], e.CtimeNS)
		}
		// every ancestor is a known directory (stop at the first one already known)
		if _, ok := byDir[dir]; !ok {
			byDir[dir] = nil
		}
		for d := niParent(dir); dir != ""; d = niParent(d) {
			if _, ok := byDir[d]; ok {
				break
			}
			byDir[d] = nil
		}
	}
	l := niListing{git: true}
	for d := range gitlinks {
		delete(byDir, d) // a gitlink path is never a tracked directory of this repo
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		abs := root
		if d != "" {
			abs = filepath.Join(root, filepath.FromSlash(d))
		}
		fi, err := os.Lstat(abs)
		if err != nil || !fi.IsDir() {
			continue // gone (its files too); a symlink is listed by its parent
		}
		dirty := d == "" || !fi.ModTime().Before(idxMod.Add(-giRacy)) || fi.ModTime().UnixNano() > newest[d]
		if !dirty {
			l.files = append(l.files, byDir[d]...)
			l.dirs = append(l.dirs, niDirState{Rel: d, ModNS: fi.ModTime().UnixNano()})
			continue
		}
		// changed since the index: this directory's entries come from disk
		des, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		l.dirs = append(l.dirs, niDirState{Rel: d, ModNS: fi.ModTime().UnixNano(), Read: true})
		for _, de := range des {
			rel := de.Name()
			if d != "" {
				rel = d + "/" + rel
			}
			if !de.IsDir() {
				l.files = append(l.files, rel)
				continue
			}
			if niSkipName(de.Name()) {
				continue
			}
			if _, known := byDir[rel]; !known {
				niWalkInto(root, rel, &l, niMaxFiles+1) // untracked tree or submodule
			}
		}
	}
	// submodules under clean directories
	for g := range gitlinks {
		dir := ""
		if j := strings.LastIndexByte(g, '/'); j >= 0 {
			dir = g[:j]
		}
		if !niDirRead(l.dirs, dir) {
			if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(g))); err == nil && fi.IsDir() {
				niWalkInto(root, g, &l, niMaxFiles+1)
			}
		}
	}
	if len(l.files) > niMaxFiles {
		return niListing{}, false // past the cap the walk decides what is kept
	}
	sort.Slice(l.files, func(i, j int) bool { return niWalkLess(l.files[i], l.files[j]) })
	l.files = niDedupSorted(l.files)
	sort.Slice(l.dirs, func(i, j int) bool { return niWalkLess(l.dirs[i].Rel, l.dirs[j].Rel) })
	return l, true
}

func niDirRead(dirs []niDirState, rel string) bool {
	for _, d := range dirs {
		if d.Rel == rel {
			return d.Read
		}
	}
	return false
}

func niDedupSorted(s []string) []string {
	if len(s) < 2 {
		return s
	}
	out := s[:1]
	for _, x := range s[1:] {
		if x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}

// niPathSkipped: some directory of rel is one the walk never enters.
func niPathSkipped(rel string) bool {
	for {
		j := strings.IndexByte(rel, '/')
		if j < 0 {
			return false
		}
		if niSkipName(rel[:j]) {
			return true
		}
		rel = rel[j+1:]
	}
}
