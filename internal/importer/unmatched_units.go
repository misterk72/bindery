package importer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// Library adoption's side of the scan: the files the reconcile loop could not
// match are grouped into books and stored as unmatched_units rows, where an
// admin can adopt, ignore or leave them (see internal/api/adoption.go).

const (
	// maxUnmatchedFiles bounds how many unmatched files one scan groups. The
	// old blob list stopped at 1000 files, which a single large audiobook
	// could fill on its own (#2547).
	maxUnmatchedFiles = 50000
	// maxUnmatchedUnits bounds how many rows one scan stores.
	maxUnmatchedUnits = 20000
	// candidateThreshold is the lowest title similarity offered as a
	// suggestion. The reconcile itself needs 0.85; below that a person decides.
	candidateThreshold = 0.60
	// maxCandidates is how many suggestions a unit keeps.
	maxCandidates = 3
)

// UnmatchedUnitStore is where the scan records its unmatched units.
type UnmatchedUnitStore interface {
	ReconcileScan(ctx context.Context, units []db.UnmatchedUnitScan, opts db.ReconcileScanOptions) (db.ReconcileScanResult, error)
	Summary(ctx context.Context) (db.UnmatchedSummary, error)
}

// WithUnmatchedUnits attaches the store the library scan writes unmatched
// units to. Nil leaves the scan counting unmatched files and storing nothing.
func (s *Scanner) WithUnmatchedUnits(store UnmatchedUnitStore) *Scanner {
	s.unmatchedUnits = store
	return s
}

// ScanRunning reports whether a library scan is in flight, for the adoption
// page's scan status.
func (s *Scanner) ScanRunning() bool {
	return s.scanRunning.Load()
}

// walkedFile is what the library walk already knew about a file (P2): its
// size and mode come from the os.FileInfo filepath.Walk hands over, so
// grouping costs no extra stat.
type walkedFile struct {
	size int64
	mode os.FileMode
}

// unmatchedScanFile is one file the reconcile loop left unmatched, with what
// the loop parsed out of it.
type unmatchedScanFile struct {
	path         string
	format       string
	size         int64
	mode         os.FileMode
	title        string
	author       string
	layoutAuthor string
	reason       string
}

// unmatchedCollector gathers unmatched files up to maxUnmatchedFiles.
type unmatchedCollector struct {
	files     []unmatchedScanFile
	truncated bool
}

func (c *unmatchedCollector) add(f unmatchedScanFile) {
	if len(c.files) >= maxUnmatchedFiles {
		c.truncated = true
		return
	}
	c.files = append(c.files, f)
}

// unmatchedGroup is one unit before candidates are ranked: the stored shape
// plus the member whose parse speaks for the unit.
type unmatchedGroup struct {
	unit db.UnmatchedUnitScan
	rep  unmatchedScanFile
}

// scanRootFor returns the longest root that contains path, or "".
func scanRootFor(path string, roots []string) string {
	best := ""
	for _, r := range roots {
		if r != "" && pathUnderDir(path, r) && len(r) > len(best) {
			best = r
		}
	}
	return best
}

// unitKeyFor decides which unit a file belongs to (#2547). The unit is a book,
// not a file:
//
//   - audio groups by its folder, so 193 tracks are one row;
//   - audio in a disc folder (CD1, Disc 2) groups by the folder above it;
//   - loose audio directly in a library root has no book folder, so each file
//     stands alone, as in the folder import scan;
//   - ebooks group by folder and file stem, so Title.epub and Title.mobi are
//     one book in two formats.
//
// It returns the grouping key, and for a folder unit the folder path.
func unitKeyFor(f unmatchedScanFile, root string) (key, folder string) {
	parent := filepath.Dir(f.path)
	if f.format == models.MediaTypeAudiobook {
		if parent == root {
			return "file\x00" + f.path, ""
		}
		if IsDiscFolderName(filepath.Base(parent)) && filepath.Dir(parent) != root && pathUnderDir(filepath.Dir(parent), root) {
			parent = filepath.Dir(parent)
		}
		return "folder\x00" + parent, parent
	}
	base := filepath.Base(f.path)
	return "stem\x00" + parent + "\x00" + strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base))), ""
}

// groupUnmatched turns unmatched files into book units. Every file lands in
// exactly one unit, and the units' file counts sum to the number of files
// given (TestGroupUnmatched_Property). Units come back ordered by path. More
// than maxUnmatchedUnits units is truncated to that many.
func groupUnmatched(files []unmatchedScanFile, roots []string) (groups []unmatchedGroup, truncated bool) {
	type acc struct {
		folder  string
		root    string
		members []unmatchedScanFile
	}
	byKey := make(map[string]*acc)
	var keys []string
	for _, f := range files {
		root := scanRootFor(f.path, roots)
		key, folder := unitKeyFor(f, root)
		a := byKey[key]
		if a == nil {
			a = &acc{folder: folder, root: root}
			byKey[key] = a
			keys = append(keys, key)
		}
		a.members = append(a.members, f)
	}

	groups = make([]unmatchedGroup, 0, len(keys))
	for _, key := range keys {
		a := byKey[key]
		slices.SortFunc(a.members, func(x, y unmatchedScanFile) int { return strings.Compare(x.path, y.path) })
		u := db.UnmatchedUnitScan{
			UnitPath:  a.members[0].path,
			UnitKind:  db.UnmatchedKindFile,
			Format:    a.members[0].format,
			FileCount: len(a.members),
			RootPath:  a.root,
		}
		if a.folder != "" {
			u.UnitPath, u.UnitKind = a.folder, db.UnmatchedKindFolder
		}
		u.MemberPaths = make([]string, len(a.members))
		for i, m := range a.members {
			u.MemberPaths[i] = m.path
			u.SizeBytes += m.size
		}
		if a.root != "" {
			if rel, err := filepath.Rel(a.root, u.UnitPath); err == nil {
				u.RelPath = filepath.ToSlash(rel)
			}
			if rel, err := filepath.Rel(a.root, a.members[0].path); err == nil {
				if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) >= 2 {
					u.AuthorFolder = parts[0]
				}
			}
		}
		rep := representative(a.members)
		// A disc set's tracks parse their title from the disc folder ("CD 1");
		// the book is the folder above it, so the unit is named after that.
		if a.folder != "" && filepath.Dir(a.members[0].path) != a.folder {
			rep.title = cleanLayoutTitle(filepath.Base(a.folder))
		}
		u.ParsedTitle, u.ParsedAuthor, u.Reason = rep.title, rep.author, rep.reason
		groups = append(groups, unmatchedGroup{unit: u, rep: rep})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].unit.UnitPath < groups[j].unit.UnitPath })
	if len(groups) > maxUnmatchedUnits {
		groups, truncated = groups[:maxUnmatchedUnits], true
	}
	return groups, truncated
}

// representative picks the member whose parse speaks for the unit: the one
// carrying the title most members agree on. For an audiobook folder that is
// the folder's title rather than one track's chapter name. Members arrive
// sorted, so ties go to the first path.
func representative(members []unmatchedScanFile) unmatchedScanFile {
	counts := make(map[string]int, len(members))
	best := members[0]
	for _, m := range members {
		counts[m.title]++
		if counts[m.title] > counts[best.title] {
			best = m
		}
	}
	return best
}

// eligibleUnmatched keeps only the files that may become rows (S10): regular
// files, by the mode the walk read with Lstat, whose resolved folder lies in
// a scanned root. A symlink inside the library would otherwise become a
// tracked book file that the download and OPDS routes serve from wherever it
// points. Each folder is resolved once.
func eligibleUnmatched(files []unmatchedScanFile, roots []string) []unmatchedScanFile {
	realRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			realRoots = append(realRoots, rr)
		}
	}
	resolved := make(map[string]bool)
	out := files[:0:0]
	for _, f := range files {
		if !f.mode.IsRegular() {
			slog.Debug("library scan: unmatched entry is not a regular file; not listing it", "path", f.path)
			continue
		}
		dir := filepath.Dir(f.path)
		ok, seen := resolved[dir]
		if !seen {
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				for _, r := range realRoots {
					if pathUnderDir(real, r) {
						ok = true
						break
					}
				}
			}
			resolved[dir] = ok
		}
		if !ok {
			slog.Debug("library scan: unmatched file resolves outside the library; not listing it", "path", f.path)
			continue
		}
		out = append(out, f)
	}
	return out
}

// rankCandidates returns up to maxCandidates catalogue books whose title
// scores at least candidateThreshold against normParsed, best first. It uses
// the reconcile's own measure (Jaro-Winkler over normalizeTitle) and its own
// candidate set: the books of the authors the file's author resolved to, or
// every reconcile candidate when the file named no author. In that last case
// titles of wildly different length are skipped, the same cheap gate the
// reconcile uses. No provider is asked anything.
func rankCandidates(normParsed string, wanted []scanBook, byAuthor map[int64][]int, authorSet map[int64]bool) []db.UnmatchedCandidate {
	if normParsed == "" {
		return nil
	}
	var out []db.UnmatchedCandidate
	consider := func(sb *scanBook) {
		score := textutil.JaroWinkler(sb.normTitle, normParsed)
		if score < candidateThreshold {
			return
		}
		out = append(out, db.UnmatchedCandidate{BookID: sb.book.ID, Score: score})
	}
	if authorSet == nil {
		for i := range wanted {
			lo, hi := wanted[i].normLen, len(normParsed)
			if lo > hi {
				lo, hi = hi, lo
			}
			if lo == 0 || lo*4 < hi {
				continue
			}
			consider(&wanted[i])
		}
	} else {
		ids := make([]int64, 0, len(authorSet))
		for id := range authorSet {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			for _, idx := range byAuthor[id] {
				consider(&wanted[idx])
			}
		}
	}
	slices.SortStableFunc(out, func(a, b db.UnmatchedCandidate) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// unitCounts is what the scan result blob reports about stored units.
type unitCounts struct {
	pending   int
	ignored   int
	truncated bool
}

// recordUnmatchedUnits groups a finished scan's unmatched files and stores
// them. candidatesFor ranks suggestions for one unit's representative parse.
func (s *Scanner) recordUnmatchedUnits(ctx context.Context, c *unmatchedCollector, roots []string, startedAt time.Time,
	candidatesFor func(title, author, layoutAuthor string) []db.UnmatchedCandidate) unitCounts {
	if s.unmatchedUnits == nil {
		return unitCounts{}
	}
	groups, unitsTruncated := groupUnmatched(eligibleUnmatched(c.files, roots), roots)
	units := make([]db.UnmatchedUnitScan, len(groups))
	for i, g := range groups {
		units[i] = g.unit
		units[i].Candidates = candidatesFor(g.rep.title, g.rep.author, g.rep.layoutAuthor)
	}
	truncated := c.truncated || unitsTruncated
	res, err := s.unmatchedUnits.ReconcileScan(ctx, units, db.ReconcileScanOptions{
		StartedAt: startedAt,
		SkipPurge: truncated,
	})
	if err != nil {
		slog.Warn("library scan: failed to store unmatched units", "error", err)
		return s.unmatchedUnitCounts(ctx, truncated)
	}
	slog.Info("library scan: stored unmatched units", "units", res.Upserted, "removed", res.RemovedPending,
		"purged", res.Purged, "pending", res.Pending, "ignored", res.Ignored, "truncated", truncated)
	return unitCounts{pending: res.Pending, ignored: res.Ignored, truncated: truncated}
}

// unmatchedUnitCounts reads the counts without changing any row, for a scan
// that found no files or failed: those must not delete anyone's decisions.
func (s *Scanner) unmatchedUnitCounts(ctx context.Context, truncated bool) unitCounts {
	if s.unmatchedUnits == nil {
		return unitCounts{}
	}
	sum, err := s.unmatchedUnits.Summary(ctx)
	if err != nil {
		slog.Warn("library scan: failed to count unmatched units", "error", err)
		return unitCounts{truncated: truncated}
	}
	return unitCounts{pending: sum.Pending, ignored: sum.Ignored, truncated: truncated}
}

// audioTrackedByFolder reports whether an audio file sits in a folder that is
// already tracked: its own folder, or, for a disc folder, the book folder
// above it. Adoption registers a disc set by its book folder, so without the
// second case the next scan would list every disc's tracks again.
func audioTrackedByFolder(trackedPaths map[string]bool, cleanPath string) bool {
	parent := filepath.Dir(cleanPath)
	if trackedPaths[parent] {
		return true
	}
	return IsDiscFolderName(filepath.Base(parent)) && trackedPaths[filepath.Dir(parent)]
}
