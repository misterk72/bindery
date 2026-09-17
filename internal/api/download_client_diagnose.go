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

// diagCheckResult is one row of the Diagnose checklist.
type diagCheckResult struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

type diagPaths struct {
	ClientPath string `json:"clientPath"`
	RemapRule  string `json:"remapRule"`
	LocalPath  string `json:"localPath"`
}

type diagHardlinkRow struct {
	Root     string `json:"root"`
	Linkable bool   `json:"linkable"`
	Reason   string `json:"reason,omitempty"`
}

type diagnoseResponse struct {
	ClientType string            `json:"clientType"`
	Checks     []diagCheckResult `json:"checks"`
	Paths      diagPaths         `json:"paths"`
	Hardlinks  []diagHardlinkRow `json:"hardlinks"`
	PrimaryFix string            `json:"primaryFix"`
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

	hardlinkProbe func(a, b string) (bool, string)

	paths      diagPaths
	probePath  string // symlink-resolved local path, set once it is known readable
	hardlinks  []diagHardlinkRow
	probeCount int
}

// diagCheck is one step of the doctor. always marks a check that still runs
// after an earlier failure because it does not depend on anything before it.
type diagCheck struct {
	code   string
	always bool
	run    func(ctx context.Context, st *diagState) diagCheckResult
}

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
	out := make([]diagCheckResult, 0, len(checks))
	failed := false
	for _, c := range checks {
		if failed && !c.always {
			out = append(out, diagCheckResult{Code: c.code, Status: diagSkipped, Message: "Skipped because an earlier check failed."})
			continue
		}
		res := c.run(ctx, st)
		res.Code = c.code
		res.Message = st.redact(res.Message)
		res.Fix = st.redact(res.Fix)
		if res.Status == diagFail {
			failed = true
		}
		out = append(out, res)
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
		hardlinkProbe:        h.hardlinkProbe,
	}
	if st.hardlinkProbe == nil {
		st.hardlinkProbe = hardlinkableReason
	}
	if h.roots != nil {
		st.libraryRoots = h.roots.resolveRoots(ctx)
	}
	checks := runDiagChecks(ctx, st, diagChecks)
	hardlinks := make([]diagHardlinkRow, 0, len(st.hardlinks))
	for _, row := range st.hardlinks {
		row.Reason = st.redact(row.Reason)
		hardlinks = append(hardlinks, row)
	}
	// Paths came from the client too, so they get the same treatment as the
	// sentences.
	paths := diagPaths{
		ClientPath: st.redact(st.paths.ClientPath),
		RemapRule:  st.paths.RemapRule,
		LocalPath:  st.redact(st.paths.LocalPath),
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

func checkDiagConfig(_ context.Context, st *diagState) diagCheckResult {
	if _, err := sanitizeHost(st.client.Host); err != nil {
		return diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("The saved host cannot be used: %s", err.Error()),
			Fix:     "Edit the client and put only a hostname or IP address in Host, with the port and URL base in their own fields.",
		}
	}
	if err := httpsec.ValidateOutboundURL(downloadClientURL(st.client), httpsec.PolicyLANLoopback); err != nil {
		return diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("Bindery will not connect to the saved address: %s", errText(err)),
			Fix:     "Use a LAN or loopback address for the download client.",
		}
	}
	if !st.client.Enabled {
		return diagCheckResult{
			Status:  diagWarn,
			Message: "This client is turned off, so Bindery sends it nothing.",
			Fix:     "Turn the client on in Settings when you want Bindery to use it.",
		}
	}
	return diagCheckResult{Status: diagPass, Message: "The saved settings are usable."}
}

func checkDiagConnect(ctx context.Context, st *diagState) diagCheckResult {
	if err := downloader.TestConnection(ctx, st.client); err != nil {
		return diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("Bindery could not connect to %s: %s", st.clientName, errText(err)),
			Fix:     "Check the host, port, URL base, TLS setting and credentials. If Bindery runs in Docker, localhost is the Bindery container itself, so use the client's LAN IP or service name.",
		}
	}
	return diagCheckResult{Status: diagPass, Message: fmt.Sprintf("Connected to %s.", st.clientName)}
}

func checkDiagCategory(ctx context.Context, st *diagState) diagCheckResult {
	report, err := downloader.CheckCategories(ctx, st.client)
	if err != nil {
		return diagCheckResult{
			Status:  diagUnknown,
			Message: fmt.Sprintf("Bindery could not read the category list from %s: %s", st.clientName, errText(err)),
		}
	}
	if !report.Checked {
		switch st.client.Type {
		case "deluge":
			return diagCheckResult{
				Status:  diagUnknown,
				Message: "Bindery cannot list Deluge labels. A label only works when Deluge's Label plugin is on.",
				Fix:     "Turn on the Label plugin in Deluge if you set a category here.",
			}
		case "transmission":
			return diagCheckResult{Status: diagPass, Message: "Transmission has no categories to set up."}
		default:
			return diagCheckResult{Status: diagPass, Message: fmt.Sprintf("%s labels need no setup in the client.", st.clientName)}
		}
	}
	if len(report.Wanted) == 0 {
		return diagCheckResult{Status: diagPass, Message: fmt.Sprintf("No category is set, so downloads use the default folder in %s.", st.clientName)}
	}
	if len(report.Missing) > 0 {
		have := "none"
		if len(report.Existing) > 0 {
			have = quoteJoin(report.Existing)
		}
		return diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("%s has no category %s. It has: %s.", st.clientName, quoteJoin(report.Missing), have),
			Fix:     fmt.Sprintf("Create the category %s in %s, or change this client's category in Bindery to one that exists.", quoteJoin(report.Missing), st.clientName),
		}
	}
	return diagCheckResult{Status: diagPass, Message: fmt.Sprintf("%s has the category %s.", st.clientName, quoteJoin(report.Wanted))}
}

func checkDiagClientPath(ctx context.Context, st *diagState) diagCheckResult {
	info, err := downloader.CompletedPath(ctx, st.client)
	if err != nil {
		res := diagCheckResult{
			Status:  diagUnknown,
			Message: fmt.Sprintf("%s did not say where it saves completed downloads: %s", st.clientName, errText(err)),
			Fix:     fmt.Sprintf("Compare the completed downloads folder in %s with Bindery's download folder by hand.", st.clientName),
		}
		if st.client.Type == "sabnzbd" || st.client.Type == "" {
			res.Message = "SABnzbd did not share its folder settings. It only does that for the full API key, not the NZB key."
			res.Fix = "Save SABnzbd's full API key on this client to run the folder checks, or compare SABnzbd's completed folder with Bindery's download folder by hand."
		}
		return res
	}
	if info.Path == "" {
		res := diagCheckResult{
			Status:  diagUnknown,
			Message: fmt.Sprintf("%s did not report a completed downloads folder Bindery can use.", st.clientName),
			Fix:     fmt.Sprintf("Set an absolute completed downloads folder in %s.", st.clientName),
		}
		if st.client.Type == "qbittorrent" {
			res.Message = fmt.Sprintf("qBittorrent reported no save path for the category %q.", strings.TrimSpace(st.client.Category))
			res.Fix = "Set a category in Bindery and give that category a save path in qBittorrent."
		}
		return res
	}
	st.paths.ClientPath = info.Path
	return diagCheckResult{
		Status:  diagPass,
		Message: fmt.Sprintf("%s saves completed downloads to %q (its %s).", st.clientName, info.Path, info.Source),
	}
}

func checkDiagRemap(_ context.Context, st *diagState) diagCheckResult {
	raw := st.paths.ClientPath
	if raw == "" {
		return diagCheckResult{Status: diagSkipped, Message: "Skipped because the client did not report a folder."}
	}
	local, rule := downloader.RemapClientPath(st.client, raw, pathmap.Parse(st.globalRemap))
	st.paths.RemapRule = rule
	if pathmap.IsWindowsPath(local) {
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
	st.paths.LocalPath = local
	if !filepath.IsAbs(local) {
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
		if p == "" || !filepath.IsAbs(p) {
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

// resolveExistingPrefix resolves symlinks in the longest prefix of p that
// exists and appends the rest unchanged.
func resolveExistingPrefix(p string) string {
	cur := p
	var rest []string
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
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
func checkDiagLocalPath(_ context.Context, st *diagState) diagCheckResult {
	local := st.paths.LocalPath
	if local == "" {
		return diagCheckResult{Status: diagSkipped, Message: "Skipped because there is no local folder to check."}
	}
	bases := st.bases()
	base, ok := containingBase(local, bases)
	if !ok {
		return outsideConfiguredFolders(st, local, bases, "")
	}
	resolved := resolveExistingPrefix(local)
	resolvedBases := make([]diagBase, 0, len(bases))
	for _, b := range bases {
		resolvedBases = append(resolvedBases, diagBase{label: b.label, path: resolveExistingPrefix(b.path)})
	}
	if base, ok = containingBase(resolved, resolvedBases); !ok {
		return outsideConfiguredFolders(st, local, bases, resolved)
	}

	info, err := os.Stat(resolved)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if found, diverged := downloader.FindCaseInsensitivePathUnder(base.path, resolved); found != "" {
			return diagCheckResult{
				Status:  diagWarn,
				Message: fmt.Sprintf("%q does not exist, but %q does. Folder names on Linux are case sensitive.", local, found),
				Fix:     fmt.Sprintf("Change the path remap, or the folder set in %s, so the part %q matches the folder on disk.", st.clientName, filepath.Base(diverged)),
			}
		}
		return diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("%q does not exist inside Bindery.", local),
			Fix:     fmt.Sprintf("Check the path remap and that the storage %s writes to is mounted into Bindery. A client that has never finished a download in this category may create the folder later.", st.clientName),
		}
	case errors.Is(err, fs.ErrPermission):
		return permissionDenied(local)
	case err != nil:
		return diagCheckResult{Status: diagFail, Message: fmt.Sprintf("Bindery cannot inspect %q: %s", local, errText(err))}
	case !info.IsDir():
		return diagCheckResult{
			Status:  diagFail,
			Message: fmt.Sprintf("%q is a file, not a folder.", local),
			Fix:     "Point the path remap at the folder the client saves into.",
		}
	}
	if err := readableDir(resolved); err != nil {
		return permissionDenied(local)
	}
	st.probePath = resolved
	if dh := config.CheckDir(resolved); !dh.Writable {
		return diagCheckResult{
			Status:  diagWarn,
			Message: fmt.Sprintf("Bindery can read %q but cannot write to it (%s). Imports that move files, and removing finished downloads, will fail.", local, dh.Reason),
			Fix:     "Give the user Bindery runs as write access to the download folder, or run Bindery as the user that owns it.",
		}
	}
	return diagCheckResult{Status: diagPass, Message: fmt.Sprintf("Bindery can read and write %q.", local)}
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

func outsideConfiguredFolders(st *diagState, local string, bases []diagBase, resolved string) diagCheckResult {
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
	fix := fmt.Sprintf("Add a path remap on this client that turns the folder %s reports into a folder under the download folder, or change where %s saves.", st.clientName, st.clientName)
	for _, b := range bases {
		n := len(b.path)
		if len(local) >= n && strings.EqualFold(local[:n], b.path) && (len(local) == n || local[n] == filepath.Separator) {
			fix = fmt.Sprintf("%q differs from the %s %q only in letter case, and folder names on Linux are case sensitive. Change the path remap so the case matches.", local, b.label, b.path)
			break
		}
	}
	return diagCheckResult{Status: diagFail, Message: msg, Fix: fix}
}

// checkDiagHardlinks reports, per library root, whether imports from the
// download folder can hardlink. Roots on the same device share one probe, so
// a library split across many folders on one disk costs one temporary file.
func checkDiagHardlinks(_ context.Context, st *diagState) diagCheckResult {
	if st.probePath == "" {
		return diagCheckResult{Status: diagSkipped, Message: "Skipped because the download folder has not been confirmed readable."}
	}
	roots := make([]string, 0, len(st.libraryRoots))
	for _, root := range st.libraryRoots {
		if root = strings.TrimSpace(root); root != "" && filepath.IsAbs(root) {
			roots = append(roots, filepath.Clean(root))
		}
	}
	if len(roots) == 0 {
		return diagCheckResult{Status: diagUnknown, Message: "No library folder is configured, so there is nothing to compare."}
	}
	sort.Strings(roots)
	type result struct {
		ok     bool
		reason string
	}
	byDevice := map[string]result{}
	failing := 0
	firstReason := ""
	for _, root := range roots {
		key := diagDeviceKey(root)
		res, seen := byDevice[key]
		if !seen {
			st.probeCount++
			ok, reason := st.hardlinkProbe(st.probePath, root)
			res = result{ok: ok, reason: reason}
			byDevice[key] = res
		}
		st.hardlinks = append(st.hardlinks, diagHardlinkRow{Root: root, Linkable: res.ok, Reason: res.reason})
		if !res.ok {
			failing++
			if firstReason == "" {
				firstReason = res.reason
			}
		}
	}
	if failing == 0 {
		return diagCheckResult{Status: diagPass, Message: "Imports from this folder can hardlink into every library folder."}
	}
	return diagCheckResult{
		Status:  diagWarn,
		Message: fmt.Sprintf("Imports into %d of %d library folders will copy instead of hardlinking: %s.", failing, len(roots), firstReason),
		Fix:     "To hardlink, mount the download folder and the library from the same filesystem into Bindery as a single volume. Copying still works, it just uses more space.",
	}
}

func checkDiagIndexerReach(_ context.Context, st *diagState) diagCheckResult {
	return diagCheckResult{
		Status:  diagUnknown,
		Message: "Bindery cannot test whether the download client can reach your indexers, trackers or Usenet servers.",
		Fix:     fmt.Sprintf("If downloads stall inside %s, check that client's own network, VPN and DNS. Bindery fetches NZB and torrent files itself, but the client downloads the content.", st.clientName),
	}
}

func quoteJoin(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(out, ", ")
}
