package main

// nameindex_idx.go: the name index's memory layout and its on-disk copy
// (low-level study §5.1/§5.3, item 8).
//
// Layout: per file one niRec (offsets and lengths into one byte blob, plus
// flags) and one niFast (lower-bound filter data); two permutations sorted
// by path and by base name replace the old maps. All of it is pointer-free
// (noscan for the GC). ix.files are string views over the blob, built in
// one pass with no allocation per file.
//
// Persistence: dataDir()/ni/<hash of root>.idx holds exactly these arrays
// (native byte order, checked by a byte-order mark) and the directories the
// listing came from with their mtimes. A later process reads the file, bounds-checks
// every offset, and maps the sections back with unsafe.Slice: no decoding.
// Validity is per directory: a directory whose mtime changed (an entry was
// created, removed or renamed), that was taken from .git/index without being
// read, or that was modified too close to the snapshot (racy) is read again;
// the files of every other directory are reused with their keys. File
// content changes do not matter (only names are indexed). Symbols are not
// persisted: they are still extracted lazily on the first identifier lookup.

import (
	"crypto/sha256"
	"encoding/hex"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"
)

// niRec locates one file's strings in the blob.
type niRec struct {
	Rel, Ext, Stem, Core, Dir uint32 // offsets
	RelN, BaseN, ExtN         uint16 // Base is the last BaseN bytes of Rel
	StemN, CoreN, DirN        uint16
	Flags                     uint32 // niRecTest | niRecDoc
}

const (
	niRecTest = 1 << iota
	niRecDoc
)

// niDirRec is a niDirState on disk (Rel in the blob).
type niDirRec struct {
	Off, N uint32
	ModNS  int64
	Read   uint32
	_      uint32
}

// niPersist turns the on-disk index on (tests turn it off by default).
var niPersist = true

// niBuild returns the index of root: the persisted copy when every
// directory is unchanged, else the listing refreshed (only changed
// directories are read; unchanged files keep their keys), else a full
// listing (.git/index when root is a work tree, gitindex.go, or WalkDir).
func niBuild(root string) *niIndex {
	t0 := time.Now()
	var ix *niIndex
	old := niLoadIdx(root)
	switch {
	case old != nil:
		dirty := old.dirtyDirs()
		if len(dirty) == 0 {
			ix = old
			break
		}
		if l, ok := old.refresh(dirty); ok {
			ix = niFromListing(root, l, old)
		}
	}
	if ix == nil {
		l, ok := niListGit(root)
		if !ok {
			l = niListWalk(root)
		}
		ix = niFromListing(root, l, nil)
	}
	if ix != old {
		niSaveIdx(ix)
	}
	ix.built = time.Now()
	ix.buildMS = float64(time.Since(t0).Microseconds()) / 1000
	return ix
}

// niFromListing keys the files of a listing; files present in old (same
// path) reuse their keys instead of folding them again.
func niFromListing(root string, l niListing, old *niIndex) *niIndex {
	if !niListingFits(l) {
		files := make([]string, 0, len(l.files))
		for _, f := range l.files {
			if len(f) <= 0xffff {
				files = append(files, f)
			}
		}
		l.files = files
	}
	n := len(l.files)
	ix := &niIndex{root: root, trunc: l.trunc, dirs: l.dirs, snapNS: time.Now().UnixNano()}
	ix.recs = make([]niRec, n)
	ix.fast = make([]niFast, n)
	blob := make([]byte, 0, n*64)
	put := func(s string) (uint32, uint16) {
		off := len(blob)
		blob = append(blob, s...)
		return uint32(off), uint16(min(len(s), 0xffff))
	}
	for i, rel := range l.files {
		var k niKeys
		if j, ok := old.relIndex(rel); ok {
			f := &old.files[j]
			k = niKeys{f.Ext, f.StemKey, f.CoreKey, f.DirKey, f.Test, f.Doc}
			ix.fast[i] = old.fast[j]
		} else {
			k = niKeysOf(rel)
			ix.fast[i] = niFastOf(k.stem, k.core)
		}
		r := &ix.recs[i]
		r.Rel, r.RelN = put(rel)
		r.BaseN = uint16(len(rel) - (strings.LastIndexByte(rel, '/') + 1))
		r.Ext, r.ExtN = put(k.ext)
		r.Stem, r.StemN = put(k.stem)
		if k.core == k.stem {
			r.Core, r.CoreN = r.Stem, r.StemN
		} else {
			r.Core, r.CoreN = put(k.core)
		}
		r.Dir, r.DirN = put(k.dir)
		if k.test {
			r.Flags |= niRecTest
		}
		if k.doc {
			r.Flags |= niRecDoc
		}
	}
	for _, d := range l.dirs {
		put(d.Rel) // dir names live in the blob too (persisted with it)
	}
	ix.blob = blob
	ix.materialize()
	ix.relOrd = niOrder(ix.files, func(f *niFile) string { return f.Rel })
	ix.baseOrd = niOrder(ix.files, func(f *niFile) string { return f.Base })
	return ix
}

// niListingFits: every path fits the 16-bit lengths of niRec.
func niListingFits(l niListing) bool {
	for _, f := range l.files {
		if len(f) > 0xffff {
			return false
		}
	}
	return true
}

// niKeys are the derived names of one file.
type niKeys struct {
	ext, stem, core, dir string
	test, doc            bool
}

// niKeysOf keys one file by its rel path (the rules of the old niBuild).
func niKeysOf(rel string) niKeys {
	name := rel
	if j := strings.LastIndexByte(rel, '/'); j >= 0 {
		name = rel[j+1:]
	}
	ext := strings.ToLower(filepath.Ext(name))
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	k := niKeys{ext: ext, stem: fzKey(stem), core: fzKey(niCore(stem)), doc: niDocExt[ext]}
	k.test = k.stem != k.core || qgIsTestFile(name)
	if j := strings.LastIndexByte(rel, '/'); j >= 0 {
		dir := rel[:j]
		k.dir = fzKey(dir)
		ld := strings.ToLower(dir)
		if strings.Contains("/"+ld+"/", "/test/") || strings.Contains("/"+ld+"/", "/tests/") || strings.Contains(ld, "__tests__") {
			k.test = true
		}
	}
	return k
}

// materialize builds files (views over blob), exts and dominant from recs.
func (ix *niIndex) materialize() {
	b := ix.blob
	str := func(off uint32, n uint16) string {
		if n == 0 {
			return ""
		}
		return unsafe.String(&b[off], int(n))
	}
	ix.files = make([]niFile, len(ix.recs))
	ix.exts = map[string]int{}
	for i := range ix.recs {
		r := &ix.recs[i]
		rel := str(r.Rel, r.RelN)
		f := &ix.files[i]
		f.Rel, f.Base, f.Ext = rel, rel[len(rel)-int(r.BaseN):], str(r.Ext, r.ExtN)
		f.StemKey, f.CoreKey, f.DirKey = str(r.Stem, r.StemN), str(r.Core, r.CoreN), str(r.Dir, r.DirN)
		f.Test, f.Doc = r.Flags&niRecTest != 0, r.Flags&niRecDoc != 0
		if f.Ext != "" {
			ix.exts[f.Ext]++
		}
	}
	best := 0
	ix.dominant = ""
	for e, n := range ix.exts {
		if niCodeExt[e] && (n > best || n == best && e < ix.dominant) {
			best, ix.dominant = n, e
		}
	}
}

func niOrder(files []niFile, key func(*niFile) string) []uint32 {
	o := make([]uint32, len(files))
	for i := range o {
		o[i] = uint32(i)
	}
	sort.SliceStable(o, func(a, b int) bool { return key(&files[o[a]]) < key(&files[o[b]]) })
	return o
}

// relIndex finds the file with this rel path (binary search; nil-safe).
func (ix *niIndex) relIndex(rel string) (int, bool) {
	if ix == nil {
		return 0, false
	}
	o := ix.relOrd
	k := sort.Search(len(o), func(i int) bool { return ix.files[o[i]].Rel >= rel })
	if k < len(o) && ix.files[o[k]].Rel == rel {
		return int(o[k]), true
	}
	return 0, false
}

// hasBase reports whether some file has this base name.
func (ix *niIndex) hasBase(name string) bool {
	o := ix.baseOrd
	k := sort.Search(len(o), func(i int) bool { return ix.files[o[i]].Base >= name })
	return k < len(o) && ix.files[o[k]].Base == name
}

// ---------- validity ----------

// dirtyDirs are the directories to read again (indexes into ix.dirs).
func (ix *niIndex) dirtyDirs() []int {
	var out []int
	racy := ix.snapNS - int64(giRacy)
	for i, d := range ix.dirs {
		abs := ix.root
		if d.Rel != "" {
			abs = filepath.Join(ix.root, filepath.FromSlash(d.Rel))
		}
		fi, err := os.Lstat(abs)
		if err != nil || !fi.IsDir() || !d.Read || fi.ModTime().UnixNano() != d.ModNS || d.ModNS >= racy {
			out = append(out, i)
		}
	}
	return out
}

// refresh is the listing after reading the dirty directories again: files
// of clean directories are kept, dirty ones come from disk, unknown
// subdirectories are walked, vanished ones dropped. ok=false: past the
// file cap (the caller does a full listing).
func (ix *niIndex) refresh(dirty []int) (niListing, bool) {
	isDirty := make(map[string]bool, len(dirty))
	for _, i := range dirty {
		isDirty[ix.dirs[i].Rel] = true
	}
	known := make(map[string]bool, len(ix.dirs))
	for _, d := range ix.dirs {
		known[d.Rel] = true
	}
	var l niListing
	for i := range ix.files {
		rel := ix.files[i].Rel
		dir := ""
		if j := strings.LastIndexByte(rel, '/'); j >= 0 {
			dir = rel[:j]
		}
		if !isDirty[dir] && known[dir] {
			l.files = append(l.files, rel)
		}
	}
	for _, d := range ix.dirs {
		if !isDirty[d.Rel] {
			l.dirs = append(l.dirs, d)
			continue
		}
		abs := ix.root
		if d.Rel != "" {
			abs = filepath.Join(ix.root, filepath.FromSlash(d.Rel))
		}
		fi, err := os.Lstat(abs)
		if err != nil || !fi.IsDir() {
			continue // gone: its files go with it
		}
		des, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		l.dirs = append(l.dirs, niDirState{Rel: d.Rel, ModNS: fi.ModTime().UnixNano(), Read: true})
		for _, de := range des {
			rel := de.Name()
			if d.Rel != "" {
				rel = d.Rel + "/" + rel
			}
			if !de.IsDir() {
				l.files = append(l.files, rel)
				continue
			}
			if niSkipName(de.Name()) || known[rel] {
				continue
			}
			niWalkInto(ix.root, rel, &l, niMaxFiles+1) // a new directory
		}
	}
	// directories whose parent vanished are dropped with it
	alive := make(map[string]bool, len(l.dirs))
	for _, d := range l.dirs {
		alive[d.Rel] = true
	}
	keep := l.dirs[:0]
	for _, d := range l.dirs {
		if d.Rel == "" || alive[niParent(d.Rel)] {
			keep = append(keep, d)
		}
	}
	l.dirs = keep
	files := l.files[:0]
	for _, f := range l.files {
		if alive[niParent(f)] {
			files = append(files, f)
		}
	}
	l.files = files
	if len(l.files) > niMaxFiles {
		return niListing{}, false
	}
	sort.Slice(l.files, func(i, j int) bool { return niWalkLess(l.files[i], l.files[j]) })
	l.files = niDedupSorted(l.files)
	sort.Slice(l.dirs, func(i, j int) bool { return niWalkLess(l.dirs[i].Rel, l.dirs[j].Rel) })
	return l, true
}

func niParent(rel string) string {
	if j := strings.LastIndexByte(rel, '/'); j >= 0 {
		return rel[:j]
	}
	return ""
}

// ---------- on disk ----------

const (
	niIdxMagic = "JZNI"
	niIdxVer   = 1
	niIdxBOM   = 0x01020304
	niIdxKeep  = 64 // .idx files kept (one per project root)
)

// niIdxHdr is the fixed header of an .idx file.
type niIdxHdr struct {
	Magic   [4]byte
	Ver     uint32
	BOM     uint32
	N, ND   uint32
	BlobLen uint32
	RootLen uint32
	CRC     uint32 // crc32 (Castagnoli) of everything after the header
	SnapNS  int64
	_       [24]byte
}

var niCRC = crc32.MakeTable(crc32.Castagnoli)

func niIdxPath(root string) string {
	h := sha256.Sum256([]byte(root))
	return filepath.Join(dataDir(), "ni", hex.EncodeToString(h[:12])+".idx")
}

func niAlign8(n int) int { return (n + 7) &^ 7 }

// niIdxLayout returns the section offsets of an .idx file.
func niIdxLayout(n, nd, blobLen, rootLen int) (recs, fast, rel, base, dirs, blob, total int) {
	recs = niAlign8(int(unsafe.Sizeof(niIdxHdr{})))
	fast = niAlign8(recs + n*int(unsafe.Sizeof(niRec{})))
	rel = niAlign8(fast + n*int(unsafe.Sizeof(niFast{})))
	base = niAlign8(rel + 4*n)
	dirs = niAlign8(base + 4*n)
	blob = niAlign8(dirs + nd*int(unsafe.Sizeof(niDirRec{})))
	total = blob + rootLen + blobLen
	return
}

func niBytesOf[T any](s []T) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*int(unsafe.Sizeof(s[0])))
}

// niSaveIdx writes ix (best effort; truncated listings are not persisted).
func niSaveIdx(ix *niIndex) {
	if !niPersist || ix.trunc || len(ix.files) == 0 || ix.root == "" {
		return
	}
	n, nd := len(ix.recs), len(ix.dirs)
	// dir names: offsets into the blob (put by niFromListing after the files)
	drecs := make([]niDirRec, nd)
	off := len(ix.blob)
	for i := nd - 1; i >= 0; i-- {
		off -= len(ix.dirs[i].Rel)
	}
	if off < 0 {
		return
	}
	for i, d := range ix.dirs {
		if string(ix.blob[off:off+len(d.Rel)]) != d.Rel {
			return // a blob that did not come from niFromListing
		}
		read := uint32(0)
		if d.Read {
			read = 1
		}
		drecs[i] = niDirRec{Off: uint32(off), N: uint32(len(d.Rel)), ModNS: d.ModNS, Read: read}
		off += len(d.Rel)
	}
	recs, fast, rel, base, dirs, blob, total := niIdxLayout(n, nd, len(ix.blob), len(ix.root))
	buf := make([]byte, total)
	h := niIdxHdr{Ver: niIdxVer, BOM: niIdxBOM, N: uint32(n), ND: uint32(nd), BlobLen: uint32(len(ix.blob)), RootLen: uint32(len(ix.root)), SnapNS: ix.snapNS}
	copy(h.Magic[:], niIdxMagic)
	copy(buf, niBytesOf([]niIdxHdr{h}))
	copy(buf[recs:], niBytesOf(ix.recs))
	copy(buf[fast:], niBytesOf(ix.fast))
	copy(buf[rel:], niBytesOf(ix.relOrd))
	copy(buf[base:], niBytesOf(ix.baseOrd))
	copy(buf[dirs:], niBytesOf(drecs))
	copy(buf[blob:], ix.root)
	copy(buf[blob+len(ix.root):], ix.blob)
	hs := int(unsafe.Sizeof(niIdxHdr{}))
	(*niIdxHdr)(unsafe.Pointer(&buf[0])).CRC = crc32.Checksum(buf[hs:], niCRC)
	p := niIdxPath(ix.root)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, buf, 0o600) != nil || os.Rename(tmp, p) != nil {
		os.Remove(tmp)
		return
	}
	niPruneIdx(filepath.Dir(p))
}

// niPruneIdx keeps the niIdxKeep most recently written indexes.
func niPruneIdx(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= niIdxKeep {
		return
	}
	type f struct {
		p string
		t time.Time
	}
	var fs []f
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".idx") {
			fs = append(fs, f{filepath.Join(dir, e.Name()), fi.ModTime()})
		}
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].t.After(fs[j].t) })
	for _, x := range fs[min(len(fs), niIdxKeep):] {
		os.Remove(x.p)
	}
}

// niLoadIdx reads the persisted index of root (nil: none, other version,
// other byte order, other root, or anything out of bounds).
func niLoadIdx(root string) *niIndex {
	if !niPersist || root == "" {
		return nil
	}
	b, err := os.ReadFile(niIdxPath(root))
	if err != nil || len(b) < int(unsafe.Sizeof(niIdxHdr{})) || uintptr(unsafe.Pointer(&b[0]))%8 != 0 {
		return nil
	}
	h := *(*niIdxHdr)(unsafe.Pointer(&b[0]))
	if string(h.Magic[:]) != niIdxMagic || h.Ver != niIdxVer || h.BOM != niIdxBOM || h.N == 0 || h.N > niMaxFiles || h.ND > 1<<20 {
		return nil
	}
	n, nd := int(h.N), int(h.ND)
	recs, fast, rel, base, dirs, blob, total := niIdxLayout(n, nd, int(h.BlobLen), int(h.RootLen))
	if total != len(b) || string(b[blob:blob+int(h.RootLen)]) != root || crc32.Checksum(b[unsafe.Sizeof(niIdxHdr{}):], niCRC) != h.CRC {
		return nil
	}
	ix := &niIndex{root: root, snapNS: h.SnapNS}
	ix.blob = b[blob+int(h.RootLen):]
	ix.recs = unsafe.Slice((*niRec)(unsafe.Pointer(&b[recs])), n)
	ix.fast = unsafe.Slice((*niFast)(unsafe.Pointer(&b[fast])), n)
	ix.relOrd = unsafe.Slice((*uint32)(unsafe.Pointer(&b[rel])), n)
	ix.baseOrd = unsafe.Slice((*uint32)(unsafe.Pointer(&b[base])), n)
	drecs := unsafe.Slice((*niDirRec)(unsafe.Pointer(&b[dirs])), nd)
	bl := uint64(len(ix.blob))
	in := func(off uint32, n uint16) bool { return uint64(off)+uint64(n) <= bl }
	for i := range ix.recs {
		r := &ix.recs[i]
		if !in(r.Rel, r.RelN) || r.RelN == 0 || r.BaseN == 0 || r.BaseN > r.RelN || !in(r.Ext, r.ExtN) || !in(r.Stem, r.StemN) || !in(r.Core, r.CoreN) || !in(r.Dir, r.DirN) {
			return nil
		}
		if ix.relOrd[i] >= h.N || ix.baseOrd[i] >= h.N {
			return nil
		}
	}
	ix.dirs = make([]niDirState, nd)
	for i, d := range drecs {
		if uint64(d.Off)+uint64(d.N) > bl {
			return nil
		}
		rel := ""
		if d.N > 0 {
			rel = unsafe.String(&ix.blob[d.Off], int(d.N))
		}
		ix.dirs[i] = niDirState{Rel: rel, ModNS: d.ModNS, Read: d.Read == 1}
	}
	ix.materialize()
	return ix
}
