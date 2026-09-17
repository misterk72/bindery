package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/config"
	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/pathmap"
)

// diagnoseBudget bounds every outbound call a single Diagnose makes. Each
// client call also keeps its own shorter transport timeout.
const diagnoseBudget = 20 * time.Second

// diagnoseFSTimeout bounds each filesystem phase (the local folder checks,
// then the hardlink probes). A dead network mount can block a stat for
// minutes; past this the check answers unknown and the request moves on,
// while the blocked call finishes in the background whenever the kernel lets
// it.
const diagnoseFSTimeout = 10 * time.Second

// Check statuses. "skipped" is reserved for checks the runner did not run
// because an earlier check failed, or that had nothing to work on.
const (
	diagPass    = "pass"
	diagWarn    = "warn"
	diagFail    = "fail"
	diagSkipped = "skipped"
	diagUnknown = "unknown"
)

// Stable check codes. The web may translate by code later; the English
// message is always present as the fallback.
const (
	diagCodeConfig       = "config"
	diagCodeConnect      = "connect"
	diagCodeCategory     = "category"
	diagCodeClientPath   = "client_path"
	diagCodeRemap        = "remap"
	diagCodeLocalPath    = "local_path"
	diagCodeHardlinks    = "hardlinks"
	diagCodeIndexerReach = "indexer_reach"
)

// diagCheckResult is one row of the Diagnose checklist. MediaType is set on
// the per folder rows (client_path, remap, local_path) when ebook and
// audiobook grabs land in different folders, and empty when they share one.
type diagCheckResult struct {
	Code      string `json:"code"`
	MediaType string `json:"mediaType,omitempty"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Fix       string `json:"fix,omitempty"`
}

type diagPathRow struct {
	MediaType  string `json:"mediaType,omitempty"`
	ClientPath string `json:"clientPath"`
	Source     string `json:"source,omitempty"`
	RemapRule  string `json:"remapRule"`
	LocalPath  string `json:"localPath"`
}

type diagHardlinkRow struct {
	MediaType    string `json:"mediaType,omitempty"`
	DownloadPath string `json:"downloadPath"`
	Root         string `json:"root"`
	Linkable     bool   `json:"linkable"`
	Reason       string `json:"reason,omitempty"`
}

type diagnoseResponse struct {
	ClientType string            `json:"clientType"`
	Checks     []diagCheckResult `json:"checks"`
	Paths      []diagPathRow     `json:"paths"`
	Hardlinks  []diagHardlinkRow `json:"hardlinks"`
	PrimaryFix string            `json:"primaryFix"`
}

// diagTarget is one folder grabs land in: ebook, audiobook, or both when
// they resolve to the same client folder.
type diagTarget struct {
	row       diagPathRow
	probePath string // symlink-resolved local path, set once readable
	stopped   bool   // an earlier per folder check had nothing to pass on
}

// diagState is what the checks share. Each check reads what earlier checks
// found and records what later ones need.
type diagState struct {
	client     *models.DownloadClient
	clientName string

	downloadDir          string
	audiobookDownloadDir string
	globalRemap          string
	libraryRoots         []string
	goos                 string

	// fsCtx is the request context, not the outbound budget, so slow client
	// calls do not eat into the filesystem phase.
	fsCtx         context.Context
	fsTimeout     time.Duration
	hardlinkProbe func(a, b string) (bool, string)

	targets   []*diagTarget
	hardlinks []diagHardlinkRow
}

// diagCheck is one step of the doctor. always marks a check that still runs
// after an earlier failure because it does not depend on anything before it.
// A check returns one result, or one per folder for the per folder checks.
type diagCheck struct {
	code   string
	always bool
	run    func(ctx context.Context, st *diagState) []diagCheckResult
}

func one(r diagCheckResult) []diagCheckResult { return []diagCheckResult{r} }

// diagChecks is the doctor, in order.
var diagChecks = []diagCheck{
	{code: diagCodeConfig, run: checkDiagConfig},
	{code: diagCodeConnect, run: checkDiagConnect},
	{code: diagCodeCategory, run: checkDiagCategory},
	{code: diagCodeClientPath, run: checkDiagClientPath},
	{code: diagCodeRemap, run: checkDiagRemap},
	{code: diagCodeLocalPath, run: checkDiagLocalPath},
	{code: diagCodeHardlinks, run: checkDiagHardlinks},
	{code: diagCodeIndexerReach, always: true, run: checkDiagIndexerReach},
}

// runDiagChecks runs checks in order. After the first failure every later
// check is reported as skipped without running, so nothing is probed on the
// strength of a step that already went wrong. Every sentence is redacted
// before it leaves the runner.
func runDiagChecks(ctx context.Context, st *diagState, checks []diagCheck) []diagCheckResult {
	out := make([]diagCheckResult, 0, len(checks)+3)
	failed := false
	for _, c := range checks {
		if failed && !c.always {
			out = append(out, diagCheckResult{Code: c.code, Status: diagSkipped, Message: "Skipped because an earlier check failed."})
			continue
		}
		for _, res := range c.run(ctx, st) {
			res.Code = c.code
			res.Message = st.redact(res.Message)
			res.Fix = st.redact(res.Fix)
			if res.Status == diagFail {
				failed = true
			}
			out = append(out, res)
		}
	}
	return out
}

// primaryFix is the fix of the first failure, or of the first warning when
// nothing failed.
func primaryFix(checks []diagCheckResult) string {
	for _, want := range []string{diagFail, diagWarn} {
		for _, c := range checks {
			if c.Status == want && c.Fix != "" {
				return c.Fix
			}
		}
	}
	return ""
}

// WithRoots attaches the library roots the hardlink rows compare against and
// the diagnose path gate accepts.
func (h *DownloadClientHandler) WithRoots(r *LibraryRoots) *DownloadClientHandler {
	h.roots = r
	return h
}

// Diagnose runs the download client doctor for a saved client. It takes the
// id only, never a path, so it cannot be pointed at an arbitrary folder, and it
// only touches the filesystem at or under a configured download folder or
// library root. It runs on demand only.
func (h *DownloadClientHandler) Diagnose(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	client, err := h.clients.GetByID(r.Context(), id)
	if err != nil || client == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "download client not found"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), diagnoseBudget)
	defer cancel()

	st := &diagState{
		client:               client,
		clientName:           downloader.ClientTypeName(client.Type),
		downloadDir:          h.downloadDir,
		audiobookDownloadDir: h.audiobookDownloadDir,
		globalRemap:          h.downloadPathRemap,
		goos:                 h.goos,
		fsCtx:                r.Context(),
		fsTimeout:            h.fsTimeout,
		hardlinkProbe:        h.hardlinkProbe,
	}
	if st.goos == "" {
		st.goos = runtime.GOOS
	}
	if st.fsTimeout <= 0 {
		st.fsTimeout = diagnoseFSTimeout
	}
	if st.hardlinkProbe == nil {
		st.hardlinkProbe = hardlinkableReason
	}
	if h.roots != nil {
		st.libraryRoots = h.roots.resolveRoots(ctx)
	}
	checks := runDiagChecks(ctx, st, diagChecks)

	// Paths and hardlink reasons came from the client too, so they get the
	// same treatment as the sentences.
	paths := make([]diagPathRow, 0, len(st.targets))
	for _, t := range st.targets {
		row := t.row
		row.ClientPath = st.redact(row.ClientPath)
		row.LocalPath = st.redact(row.LocalPath)
		paths = append(paths, row)
	}
	hardlinks := make([]diagHardlinkRow, 0, len(st.hardlinks))
	for _, row := range st.hardlinks {
		row.DownloadPath = st.redact(row.DownloadPath)
		row.Reason = st.redact(row.Reason)
		hardlinks = append(hardlinks, row)
	}
	writeJSON(w, http.StatusOK, diagnoseResponse{
		ClientType: client.Type,
		Checks:     checks,
		Paths:      paths,
		Hardlinks:  hardlinks,
		PrimaryFix: primaryFix(checks),
	})
}

// redact strips secrets from a sentence built from a client error. Query
// string keys go through httpsec.RedactSecrets; the stored API key and
// password are also removed verbatim in case a client echoes them some other
// way. Very short secrets are left alone because replacing a two letter
// string would garble every sentence while protecting almost nothing.
func (st *diagState) redact(s string) string {
	if s == "" {
		return s
	}
	s = httpsec.RedactSecrets(s)
	for _, secret := range []string{st.client.APIKey, st.client.Password} {
		if len(secret) >= 4 {
			s = strings.ReplaceAll(s, secret, "REDACTED")
		}
	}
	return s
}

func errText(err error) string {
	return httpsec.RedactSecrets(httpsec.RedactURLError(err).Error())
}

// boundedFS runs fn with a deadline. fn must only return values and never
// write shared state, because on a timeout it keeps running in its goroutine
// after this returns ok false.
func boundedFS[T any](ctx context.Context, timeout time.Duration, fn func() T) (T, bool) {
	ch := make(chan T, 1)
	go func() { ch <- fn() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v, true
	case <-timer.C:
	case <-ctx.Done():
	}
	var zero T
	return zero, false
}

func (st *diagState) fsTimeoutResult(what string) diagCheckResult {
	return diagCheckResult{
		Status:  diagUnknown,
		Message: fmt.Sprintf("%s did not respond within %d seconds. A network mount that has stopped answering looks like this.", what, int(st.fsTimeout.Seconds())),
		Fix:     "Check that the storage behind this folder is mounted and answering, then run Diagnose again.",
	}
}

// mediaPhrase is the media type as it reads in front of "downloads".
func mediaPhrase(mediaType string) string {
	switch mediaType {
	case models.MediaTypeEbook:
		return "ebook "
	case models.MediaTypeAudiobook:
		return "audiobook "
	default:
		return ""
	}
}

func checkDiagConfig(_ context.Context, st *diagState) []diagCheckResult {
	if _, err := sanitizeHost(st.client.Host); err != nil {
		return one(diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("The saved host cannot be used: %s", err.Error()),
			Fix:     "Edit the client and put only a hostname or IP address in Host, with the port and URL base in their own fields.",
		})
	}
	if err := httpsec.ValidateOutboundURL(downloadClientURL(st.client), httpsec.PolicyLANLoopback); err != nil {
		return one(diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("Bindery will not connect to the saved address: %s", errText(err)),
			Fix:     "Use a LAN or loopback address for the download client.",
		})
	}
	if !st.client.Enabled {
		return one(diagCheckResult{
			Status:  diagWarn,
			Message: "This client is turned off, so Bindery sends it nothing.",
			Fix:     "Turn the client on in Settings when you want Bindery to use it.",
		})
	}
	return one(diagCheckResult{Status: diagPass, Message: "The saved settings are usable."})
}

func checkDiagConnect(ctx context.Context, st *diagState) []diagCheckResult {
	if err := downloader.TestConnection(ctx, st.client); err != nil {
		return one(diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("Bindery could not connect to %s: %s", st.clientName, errText(err)),
			Fix:     "Check the host, port, URL base, TLS setting and credentials. If Bindery runs in Docker, localhost is the Bindery container itself, so use the client's LAN IP or service name.",
		})
	}
	return one(diagCheckResult{Status: diagPass, Message: fmt.Sprintf("Connected to %s.", st.clientName)})
}

func checkDiagCategory(ctx context.Context, st *diagState) []diagCheckResult {
	report, err := downloader.CheckCategories(ctx, st.client)
	if err != nil {
		return one(diagCheckResult{
			Status:  diagUnknown,
			Message: fmt.Sprintf("Bindery could not read the category list from %s: %s", st.clientName, errText(err)),
		})
	}
	if !report.Checked {
		switch st.client.Type {
		case "deluge":
			return one(diagCheckResult{
				Status:  diagUnknown,
				Message: "Bindery cannot list Deluge labels. A label only works when Deluge's Label plugin is on.",
				Fix:     "Turn on the Label plugin in Deluge if you set a category here.",
			})
		case "transmission":
			return one(diagCheckResult{Status: diagPass, Message: "Transmission has no categories to set up."})
		default:
			return one(diagCheckResult{Status: diagPass, Message: fmt.Sprintf("%s labels need no setup in the client.", st.clientName)})
		}
	}
	if len(report.Wanted) == 0 {
		return one(diagCheckResult{Status: diagPass, Message: "No category is set on this client in Bindery."})
	}
	if len(report.Missing) > 0 {
		have := "none"
		if len(report.Existing) > 0 {
			have = quoteJoin(report.Existing)
		}
		return one(diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("%s has no category %s. It has: %s.", st.clientName, quoteJoin(report.Missing), have),
			Fix:     fmt.Sprintf("Create the category %s in %s, or change this client's category in Bindery to one that exists.", quoteJoin(report.Missing), st.clientName),
		})
	}
	return one(diagCheckResult{Status: diagPass, Message: fmt.Sprintf("%s has the category %s.", st.clientName, quoteJoin(report.Wanted))})
}

// checkDiagClientPath works out where ebook and audiobook grabs land, using
// the same category and save path SendDownload uses for each, and keeps one
// folder when both resolve to the same place.
func checkDiagClientPath(ctx context.Context, st *diagState) []diagCheckResult {
	grabKey := func(mediaType string) string {
		return downloader.ResolveCategory(st.client, mediaType) + "\x00" +
			downloader.TargetDownloadDir(mediaType, st.downloadDir, st.audiobookDownloadDir)
	}
	ebook, ebookErr := downloader.GrabSavePath(ctx, st.client, models.MediaTypeEbook, st.downloadDir, st.audiobookDownloadDir)
	type resolved struct {
		mediaType string
		info      downloader.ClientPathInfo
		err       error
	}
	var found []resolved
	if grabKey(models.MediaTypeEbook) == grabKey(models.MediaTypeAudiobook) {
		found = []resolved{{"", ebook, ebookErr}}
	} else {
		audio, audioErr := downloader.GrabSavePath(ctx, st.client, models.MediaTypeAudiobook, st.downloadDir, st.audiobookDownloadDir)
		if ebookErr == nil && audioErr == nil && ebook.Path == audio.Path && ebook.Source == audio.Source && ebook.Note == audio.Note {
			found = []resolved{{"", ebook, nil}}
		} else {
			found = []resolved{{models.MediaTypeEbook, ebook, ebookErr}, {models.MediaTypeAudiobook, audio, audioErr}}
		}
	}

	out := make([]diagCheckResult, 0, len(found))
	for _, f := range found {
		t := &diagTarget{row: diagPathRow{MediaType: f.mediaType, ClientPath: f.info.Path, Source: f.info.Source}}
		st.targets = append(st.targets, t)
		res := describeClientPath(st, f.mediaType, f.info, f.err)
		res.MediaType = f.mediaType
		if res.Status != diagPass && res.Status != diagWarn {
			t.stopped = true
		}
		out = append(out, res)
	}
	return out
}

func describeClientPath(st *diagState, mediaType string, info downloader.ClientPathInfo, err error) diagCheckResult {
	media := mediaPhrase(mediaType)
	if err != nil {
		if st.client.Type == "sabnzbd" || st.client.Type == "" {
			return diagCheckResult{
				Status:  diagUnknown,
				Message: "SABnzbd did not share its folder settings. It only does that for the full API key, not the NZB key.",
				Fix:     "Save SABnzbd's full API key on this client to run the folder checks, or compare SABnzbd's completed folder with Bindery's download folder by hand.",
			}
		}
		return diagCheckResult{
			Status:  diagUnknown,
			Message: fmt.Sprintf("%s did not say where it saves completed %sdownloads: %s", st.clientName, media, errText(err)),
			Fix:     fmt.Sprintf("Compare the completed downloads folder in %s with Bindery's download folder by hand.", st.clientName),
		}
	}
	if info.Path == "" {
		res := diagCheckResult{
			Status:  diagUnknown,
			Message: fmt.Sprintf("%s did not report a completed %sdownloads folder Bindery can use.", st.clientName, media),
			Fix:     fmt.Sprintf("Set an absolute completed downloads folder in %s.", st.clientName),
		}
		if st.client.Type == "qbittorrent" && info.Category != "" {
			res.Message = fmt.Sprintf("qBittorrent has no usable save path for the category %q.", info.Category)
			res.Fix = "Give that category a save path in qBittorrent, or set a default save path."
		}
		return res
	}
	res := diagCheckResult{
		Status:  diagPass,
		Message: fmt.Sprintf("Completed %sdownloads land in %q, from %s.", media, info.Path, info.Source),
	}
	if info.Note != "" {
		res.Status = diagWarn
		res.Message += " " + info.Note
		res.Fix = fmt.Sprintf("Check the settings of that label in %s by hand.", st.clientName)
	}
	return res
}

// isAbsLocal reports whether p is absolute on the platform Bindery runs on.
func isAbsLocal(p, goos string) bool {
	return filepath.IsAbs(p) || (goos == "windows" && pathmap.IsWindowsPath(p))
}

func checkDiagRemap(_ context.Context, st *diagState) []diagCheckResult {
	if len(st.targets) == 0 {
		return one(diagCheckResult{Status: diagSkipped, Message: "Skipped because the client did not report a folder."})
	}
	global := pathmap.Parse(st.globalRemap)
	out := make([]diagCheckResult, 0, len(st.targets))
	for _, t := range st.targets {
		res := remapTarget(st, t, global)
		res.MediaType = t.row.MediaType
		out = append(out, res)
	}
	return out
}

func remapTarget(st *diagState, t *diagTarget, global *pathmap.Remapper) diagCheckResult {
	if t.stopped {
		return diagCheckResult{Status: diagSkipped, Message: "Skipped because the client did not report a folder."}
	}
	raw := t.row.ClientPath
	local, rule := downloader.RemapClientPath(st.client, raw, global)
	t.row.RemapRule = rule
	// A drive path can only exist on a Windows Bindery. Anywhere else it
	// means a remap is missing.
	if st.goos != "windows" && pathmap.IsWindowsPath(local) {
		t.stopped = true
		example := raw + ":/downloads"
		if st.downloadDir != "" {
			example = raw + ":" + st.downloadDir
		}
		return diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("%s reports the Windows path %q and no path remap translates it, so Bindery cannot find it.", st.clientName, raw),
			Fix:     fmt.Sprintf("Add a path remap on this client from the Windows folder to the folder Bindery sees, for example %q.", example),
		}
	}
	local = filepath.Clean(local)
	t.row.LocalPath = local
	if !isAbsLocal(local, st.goos) {
		t.stopped = true
		return diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("The folder resolves to %q, which is not an absolute path.", local),
			Fix:     "Make both sides of the path remap absolute paths.",
		}
	}
	switch rule {
	case downloader.RemapRuleClient:
		return diagCheckResult{Status: diagPass, Message: fmt.Sprintf("This client's path remap turns %q into %q.", raw, local)}
	case downloader.RemapRuleGlobal:
		return diagCheckResult{Status: diagPass, Message: fmt.Sprintf("The global path remap (BINDERY_DOWNLOAD_PATH_REMAP) turns %q into %q.", raw, local)}
	default:
		return diagCheckResult{Status: diagPass, Message: fmt.Sprintf("No path remap applies, so Bindery looks for %q at the same path.", local)}
	}
}

// diagBase is a configured folder the diagnose action may inspect.
type diagBase struct {
	label string
	path  string
}

func (st *diagState) bases() []diagBase {
	var out []diagBase
	add := func(label, p string) {
		p = strings.TrimSpace(p)
		if p == "" || !isAbsLocal(p, st.goos) {
			return
		}
		out = append(out, diagBase{label: label, path: filepath.Clean(p)})
	}
	add("download folder", st.downloadDir)
	add("audiobook download folder", st.audiobookDownloadDir)
	for _, root := range st.libraryRoots {
		add("library folder", root)
	}
	return out
}

// containingBase returns the most specific configured folder p is at or
// under, or ok false. It is a pure string check and touches nothing.
func containingBase(p string, bases []diagBase) (diagBase, bool) {
	var best diagBase
	found := false
	for _, b := range bases {
		if downloader.PathIsAtOrUnder(p, b.path) && (!found || len(b.path) > len(best.path)) {
			best, found = b, true
		}
	}
	return best, found
}

// errDanglingLink marks a path whose existing part includes a symlink that
// does not resolve. Following it later could land anywhere once its target
// appears, so the caller refuses it.
var errDanglingLink = errors.New("a symbolic link in the path does not resolve")

// resolveExistingPrefix resolves symlinks in the longest prefix of p that
// exists and appends the rest unchanged. When a component exists but will not
// resolve (a dangling or looping symlink) it refuses with errDanglingLink
// instead of stepping past it to the parent.
func resolveExistingPrefix(p string) (string, error) {
	cur := p
	var rest []string
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...), nil
		}
		if info, err := os.Lstat(cur); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errDanglingLink
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// checkDiagLocalPath is the only check that looks at the filesystem through a
// path a download client chose, so it gates first. The stat, the listing for a
// letter case mismatch and the write probe run only when the remapped path is
// at or under a configured download folder or library root, both as written
// and after following symlinks. Anywhere else, the answer is that the path is
// outside every configured folder, and nothing is touched.
func checkDiagLocalPath(_ context.Context, st *diagState) []diagCheckResult {
	if len(st.targets) == 0 {
		return one(diagCheckResult{Status: diagSkipped, Message: "Skipped because there is no local folder to check."})
	}
	bases := st.bases()
	out := make([]diagCheckResult, 0, len(st.targets))
	for _, t := range st.targets {
		var res diagCheckResult
		switch {
		case t.stopped || t.row.LocalPath == "":
			res = diagCheckResult{Status: diagSkipped, Message: "Skipped because there is no local folder to check."}
		default:
			local := t.row.LocalPath
			if _, ok := containingBase(local, bases); !ok {
				res = outsideConfiguredFolders(st.clientName, local, bases, "")
				break
			}
			probed, ok := boundedFS(st.fsCtx, st.fsTimeout, func() localProbe {
				return probeLocalPath(st.clientName, local, bases)
			})
			if !ok {
				res = st.fsTimeoutResult(fmt.Sprintf("%q", local))
				break
			}
			res = probed.res
			t.probePath = probed.probePath
		}
		res.MediaType = t.row.MediaType
		out = append(out, res)
	}
	return out
}

type localProbe struct {
	res       diagCheckResult
	probePath string
}

// probeLocalPath does the filesystem work for one folder that is already
// known to be lexically inside a configured folder. It touches no shared
// state, so it is safe to abandon on a timeout.
func probeLocalPath(clientName, local string, bases []diagBase) localProbe {
	resolved, err := resolveExistingPrefix(local)
	if err != nil {
		return localProbe{res: diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("%q goes through a symbolic link that does not resolve. Bindery did not follow it.", local),
			Fix:     "Point the path remap, or the folder the client saves into, at a real folder rather than a broken link.",
		}}
	}
	resolvedBases := make([]diagBase, 0, len(bases))
	for _, b := range bases {
		rb, err := resolveExistingPrefix(b.path)
		if err != nil {
			rb = b.path
		}
		resolvedBases = append(resolvedBases, diagBase{label: b.label, path: rb})
	}
	base, ok := containingBase(resolved, resolvedBases)
	if !ok {
		return localProbe{res: outsideConfiguredFolders(clientName, local, bases, resolved)}
	}

	info, err := os.Stat(resolved)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if found, diverged := downloader.FindCaseInsensitivePathUnder(base.path, resolved); found != "" {
			return localProbe{res: diagCheckResult{
				Status:  diagWarn,
				Message: fmt.Sprintf("%q does not exist, but %q does. Folder names on Linux are case sensitive.", local, found),
				Fix:     fmt.Sprintf("Change the path remap, or the folder set in %s, so the part %q matches the folder on disk.", clientName, filepath.Base(diverged)),
			}}
		}
		return localProbe{res: diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("%q does not exist inside Bindery.", local),
			Fix:     fmt.Sprintf("Check the path remap and that the storage %s writes to is mounted into Bindery. A client that has never finished a download in this folder may create it later.", clientName),
		}}
	case errors.Is(err, fs.ErrPermission):
		return localProbe{res: permissionDenied(local)}
	case err != nil:
		return localProbe{res: diagCheckResult{Status: diagFail, Message: fmt.Sprintf("Bindery cannot inspect %q: %s", local, errText(err))}}
	case !info.IsDir():
		return localProbe{res: diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("%q is a file, not a folder.", local),
			Fix:     "Point the path remap at the folder the client saves into.",
		}}
	}
	if err := readableDir(resolved); err != nil {
		return localProbe{res: permissionDenied(local)}
	}
	if dh := config.CheckDir(resolved); !dh.Writable {
		return localProbe{probePath: resolved, res: diagCheckResult{
			Status:  diagWarn,
			Message: fmt.Sprintf("Bindery can read %q but cannot write to it (%s). Imports that move files, and removing finished downloads, will fail.", local, dh.Reason),
			Fix:     "Give the user Bindery runs as write access to the download folder, or run Bindery as the user that owns it.",
		}}
	}
	return localProbe{probePath: resolved, res: diagCheckResult{Status: diagPass, Message: fmt.Sprintf("Bindery can read and write %q.", local)}}
}

func readableDir(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func permissionDenied(local string) diagCheckResult {
	who := "the user Bindery runs as"
	if uid := os.Getuid(); uid >= 0 {
		who = fmt.Sprintf("uid %d, gid %d, which Bindery runs as,", uid, os.Getgid())
	}
	return diagCheckResult{
		Status:  diagFail,
		Message: fmt.Sprintf("Bindery cannot read %q: permission denied.", local),
		Fix:     fmt.Sprintf("Give %s read and write access to the folder, or run Bindery as the user that owns it (in Compose, set user to that uid and gid).", who),
	}
}

func outsideConfiguredFolders(clientName, local string, bases []diagBase, resolved string) diagCheckResult {
	names := make([]string, 0, len(bases))
	for _, b := range bases {
		names = append(names, fmt.Sprintf("%s %q", b.label, b.path))
	}
	configured := "no folders are configured"
	if len(names) > 0 {
		configured = strings.Join(names, ", ")
	}
	msg := fmt.Sprintf("Bindery would look for completed downloads in %q, which is outside every folder it is configured to use (%s). Bindery did not look inside it.", local, configured)
	if resolved != "" {
		msg = fmt.Sprintf("%q follows a symbolic link to %q, which is outside every folder Bindery is configured to use (%s). Bindery did not look inside it.", local, resolved, configured)
	}
	fix := fmt.Sprintf("Add a path remap on this client that turns the folder %s reports into a folder under the download folder, or change where %s saves.", clientName, clientName)
	for _, b := range bases {
		n := len(b.path)
		if len(local) >= n && strings.EqualFold(local[:n], b.path) && (len(local) == n || local[n] == filepath.Separator) {
			fix = fmt.Sprintf("%q differs from the %s %q only in letter case, and folder names on Linux are case sensitive. Change the path remap so the case matches.", local, b.label, b.path)
			break
		}
	}
	return diagCheckResult{Status: diagFail, Message: msg, Fix: fix}
}

// checkDiagHardlinks reports, for each download folder and library root pair,
// whether imports can hardlink. Every pair gets its own real link probe: two
// bind mounts of one filesystem share a device ID and still refuse links
// across them, which is exactly what the probe exists to catch.
func checkDiagHardlinks(_ context.Context, st *diagState) []diagCheckResult {
	var sources []*diagTarget
	for _, t := range st.targets {
		if t.probePath != "" {
			sources = append(sources, t)
		}
	}
	if len(sources) == 0 {
		return one(diagCheckResult{Status: diagSkipped, Message: "Skipped because no download folder has been confirmed readable."})
	}
	roots := make([]string, 0, len(st.libraryRoots))
	for _, root := range st.libraryRoots {
		if root = strings.TrimSpace(root); root != "" && isAbsLocal(root, st.goos) {
			roots = append(roots, filepath.Clean(root))
		}
	}
	if len(roots) == 0 {
		return one(diagCheckResult{Status: diagUnknown, Message: "No library folder is configured, so there is nothing to compare."})
	}
	sort.Strings(roots)

	type pair struct {
		mediaType, local, probe, root string
	}
	var pairs []pair
	for _, t := range sources {
		for _, root := range roots {
			pairs = append(pairs, pair{t.row.MediaType, t.row.LocalPath, t.probePath, root})
		}
	}
	probe := st.hardlinkProbe
	rows, ok := boundedFS(st.fsCtx, st.fsTimeout, func() []diagHardlinkRow {
		out := make([]diagHardlinkRow, 0, len(pairs))
		done := map[[2]string]diagHardlinkRow{}
		for _, p := range pairs {
			key := [2]string{p.probe, p.root}
			row, seen := done[key]
			if !seen {
				linkable, reason := probe(p.probe, p.root)
				row = diagHardlinkRow{Linkable: linkable, Reason: reason}
				done[key] = row
			}
			row.MediaType, row.DownloadPath, row.Root = p.mediaType, p.local, p.root
			out = append(out, row)
		}
		return out
	})
	if !ok {
		return one(st.fsTimeoutResult("The hardlink test"))
	}
	st.hardlinks = rows

	failing := 0
	firstReason := ""
	for _, row := range rows {
		if !row.Linkable {
			failing++
			if firstReason == "" {
				firstReason = row.Reason
			}
		}
	}
	if failing == 0 {
		return one(diagCheckResult{Status: diagPass, Message: "Imports can hardlink into every library folder."})
	}
	return one(diagCheckResult{
		Status:  diagWarn,
		Message: fmt.Sprintf("%d of %d download and library folder pairs will copy instead of hardlinking: %s.", failing, len(rows), firstReason),
		Fix:     "To hardlink, mount the download folder and the library from the same filesystem into Bindery as a single volume. Copying still works, it just uses more space.",
	})
}

func checkDiagIndexerReach(_ context.Context, st *diagState) []diagCheckResult {
	return one(diagCheckResult{
		Status:  diagUnknown,
		Message: "Bindery cannot test whether the download client can reach your indexers, trackers or Usenet servers.",
		Fix:     fmt.Sprintf("If downloads stall inside %s, check that client's own network, VPN and DNS. Bindery fetches NZB and torrent files itself, but the client downloads the content.", st.clientName),
	})
}

func quoteJoin(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(out, ", ")
}
