# Calibre and Calibre-Web-Automated

Bindery can hand a freshly imported book to Calibre, or to Calibre-Web-Automated (CWA), in three different ways. They look alike in the settings, they are configured on two different tabs, and picking the wrong one is the most common reason a book is grabbed and imported yet never shows up in Calibre. This page tells the three apart, says what each one does on disk, and ends with the troubleshooting most people arrive here for.

## Why this page exists: Calibre only knows what is in `metadata.db`

Calibre does not watch its library folder. A file placed under `/calibre-library/Author/Title/` by hand, or by Bindery, is invisible to Calibre and to CWA until something registers it in `metadata.db`. Every way of connecting Bindery to Calibre is therefore one of two things:

- a **registration call**: Bindery tells Calibre about the file (`calibredb add`, or the Bindery Bridge plugin), and Calibre copies it into its own tree and records it in `metadata.db`
- a **copy into a watched folder**: Bindery drops a file where CWA's ingest watcher (or another tool) will find it, and that tool does the registration

"Bindery writes into the Calibre library directory" is not one of the options, and setting `BINDERY_LIBRARY_DIR` to the Calibre library does not make Calibre see the files.

## The three topologies at a glance

| Topology | Who owns the library | What Bindery does on import | Where the settings live | Copies on disk | Formats |
|---|---|---|---|---|---|
| **1. Register with Calibre** | Bindery owns its library; Calibre owns a separate one | Places the file in the Bindery library, then calls `calibredb add` or POSTs to the Bridge plugin | Settings, Calibre tab, **Write integration** (`calibre.mode`) | Two: Bindery's copy and the one Calibre makes inside its library | Ebooks. The hook also fires after an audiobook import, handing over the audiobook folder |
| **2. Mirror into the CWA ingest folder** | Bindery owns its library; CWA owns a separate one | Places the file in the Bindery library, then copies it into the ingest folder; CWA consumes and deletes that copy | Settings, Calibre tab, **Calibre-Web-Automated (CWA)**, Ingest folder path (`cwa.ingest_path`) | Two: Bindery's copy and the one CWA files into its library. The ingest copy is transient | Ebooks only. Audiobooks are never mirrored |
| **3. An external tool owns the library** | CWA, Calibre or Storyteller owns the only library | Does not write into the library at all; copies or hardlinks the finished download into a drop folder and waits for the tool to file it | Settings, General tab, File Naming, **Import Mode** set to External, then Drop folder, Layout, Placement | The managed copy the external tool makes, plus a drop folder copy until that tool consumes it (the default `copy` placement), on top of the download, which is never moved | Ebooks and audiobooks |

Topologies 1 and 2 are independent switches and both run in Auto, Move, Copy and Hardlink import modes. Topology 3 replaces Bindery's own import, and topologies 1 and 2 do not run in it.

## Topology 1: register with Calibre

Bindery imports the file into its own library as usual, then tells Calibre about it. Calibre does what it always does with a new book: it copies the file into its own library tree and records it in `metadata.db`. The file now exists twice, once in the Bindery library (the author's root folder, falling back to `BINDERY_LIBRARY_DIR`, or to `BINDERY_AUDIOBOOK_DIR` for audiobooks) and once under the Calibre library, and that is expected. It is what makes Calibre's own edits, conversions and deletions safe: they never touch Bindery's copy.

The `calibredb` variant needs **Library path** on the Calibre tab set to the directory that holds `metadata.db`, as seen from inside the Bindery container. The plugin variant does not: the plugin adds to whatever library its Calibre has open.

### `calibredb` CLI

Bindery shells out to `calibredb add --with-library <lib> <metadata...> <file>`, where the metadata arguments carry the title, authors, cover, identifiers, language, series, series index and tags Bindery holds, and then sets the remaining fields on the new Calibre id. Requirements:

- the Calibre library path must be visible inside the Bindery container or process
- `calibredb` must be on `PATH`, or **Binary path (optional)** (`calibre.binary_path`) must point at it
- the official Docker image is distroless and does **not** ship `calibredb`. Either bind mount a Calibre install into the container and set the binary path, or run the Bindery binary on a host that has Calibre installed. `calibredb unreachable` from **Test connection** means this requirement is not met.

Only one process should write a Calibre library at a time. If the Calibre desktop app or Calibre-Web has the same library open, `calibredb add` may fail or the other program may not see the new book until it reloads.

### Calibre Bridge plugin

The [Bindery Bridge plugin](https://github.com/vavallee/bindery-plugins) runs inside a Calibre process (desktop or server) in another container or on another host, and Bindery POSTs to it. Points that decide whether it works:

- **The file is not uploaded.** Bindery sends the file's path (plus the book's metadata; an older plugin that rejects the metadata payload gets a path only retry) and Calibre opens that path itself. So the Bindery library must be mounted into the Calibre container too. If it sits at a different path there, set **Push path remap** (`calibre.push_path_remap`) as `from:to` pairs, for example `/books:/mnt/user/media/books`.
- **A 409 from the plugin counts as success.** It means Calibre already has the book; Bindery records the returned Calibre id and moves on. This is what makes re pushing idempotent.
- **Push all to Calibre** is only offered in plugin mode. It walks every imported book and pushes its file; books already in Calibre are skipped through the 409 path. The button stays disabled until **Test connection** (or the silent probe) has reached the bridge.

## Topology 2: mirror into the CWA ingest folder

Set **Ingest folder path** under the Calibre-Web-Automated heading on the Calibre tab to the folder CWA watches (CWA's docs use `/cwa-book-ingest`), mounted into both containers at that path. After every successful ebook import Bindery copies the imported file there. CWA picks it up, files it into its own library, and deletes the ingest copy. What the code does, exactly:

- it is always a **copy**, never a move or a hardlink, because CWA deletes whatever it consumes and Bindery's own library must stay intact
- the copy lands in the folder root under the file's **flat basename**, whatever your naming template produced, so two books with the same file name will collide in the ingest folder
- it runs for **ebooks only**; audiobooks are never mirrored
- it runs in Auto, Move, Copy and Hardlink import modes, and does **nothing in External mode** (see the next section for that setup)
- it is **independent of the write integration**: it runs whether `calibre.mode` is Off, `calibredb` or plugin. Pointing both at the same Calibre library means the same book reaches it by two routes; read the duplicates note in troubleshooting before turning both on.

This is the topology for "Bindery keeps my library, CWA should also see new ebooks".

## Topology 3: an external tool owns the library

Choose this when CWA, Calibre auto ingest or Storyteller should be the only thing writing the library and Bindery should stay out of it. Under Settings, General tab, File Naming, set **Import Mode** to `External`; the drop folder fields appear underneath.

- **Drop folder** (`import.drop_folder`) is the folder the tool ingests from, for example `/cwa-book-ingest`. Empty means Bindery hands off in place: the download stays in the download directory and nothing is copied anywhere.
- **Layout** (`import.drop_layout`): `flat` puts every book file the download carries in the folder root under a sanely named name, which is what watch folder tools expect, so a download holding two ebook files produces two; `templated` recreates the `{Author}/{Title (Year)}/…` tree from your naming template inside the drop folder.
- **Placement** (`import.drop_link_mode`): `copy` (the default, safest since the tool usually deletes what it consumes) or `hardlink` (no extra disk, same filesystem only). The download itself is never moved, so torrents keep seeding.
- **Pair gating** (`import.drop_pair_gating`, off by default) holds the first format of a book wanted in both formats until its sibling arrives, so a tool such as Storyteller that pairs an ebook with its audiobook ingests them together. `import.drop_pair_gating_timeout_hours` (default 72) releases a held format that waited alone too long. There is no toggle for it in the UI yet; set it with `PUT /api/v1/setting/import.drop_pair_gating` and body `{"value": "true"}`, which is an admin only route, so use an admin session cookie or an admin API key.

After the drop Bindery parks the download as *handed off* and the book stays Wanted until the next **library scan** finds the managed copy the tool produced. That only works if `BINDERY_LIBRARY_DIR` (and `BINDERY_AUDIOBOOK_DIR`) point at where the tool finally writes, not at the drop folder and not at `metadata.db`.

The write integration and the CWA mirror do not run in this mode; the drop folder is the whole hand off.

## Reading an existing Calibre library

Separate from all of the above, and it works alongside any topology: **Library import** on the Calibre tab reads `metadata.db` and creates authors, books and editions from it, with the file paths tracked directly so those books arrive with their files attached. It does not need the write integration to be on. See [Bringing in an existing library](User-Guide-Wiki.md#bringing-in-an-existing-library) in the user guide.

## Troubleshooting

**Grabbed and imported, but the book never appears in Calibre or CWA.**

1. Check which topology you actually configured. The most common gap is none of them: `calibre.mode` Off, no ingest folder, import mode not External. Bindery then places the file in its own library and stops, and Calibre has no way to know.
2. If you pointed `BINDERY_LIBRARY_DIR` at the Calibre library folder expecting Calibre to notice, see the first section: it will not. Move Bindery's library elsewhere and pick a topology.
3. Write integration on `calibredb`: run **Test connection**. `calibredb unreachable` means the binary is not inside the container (distroless image) or the path is wrong. The log line `calibre: add failed, continuing` carries calibredb's own output, but read it before assuming a fault: when calibredb skips a file it already has, it prints no `Added book ids` line and Bindery reports that as the same failure.
4. Write integration on plugin: run **Test connection**. If it passes but pushes fail, the Calibre container cannot open the path Bindery sent; mount the Bindery library into it or set **Push path remap**. The log line `plugin client: server error` carries the plugin's own message. The plugin path has a separate line for the harmless case, `calibre: book already in library`, so a duplicate there is never logged as a failure.
5. CWA mirror: only ebooks are copied, and only outside External mode. Check the log for `cwa: file copied to ingest folder` or `cwa: copy to ingest folder failed`, and confirm the ingest path is the same mount in both containers.
6. External mode with a drop folder: confirm the tool consumed the file, then check the Bindery log after the next library scan. If the book stays Wanted, `BINDERY_LIBRARY_DIR` is not where the tool writes.

**The same book reaches Calibre by two routes.** Topology 1 and topology 2 are both on against the same Calibre library, or `Push all to Calibre` ran against a library the ingest folder also feeds. Whether that ends as one row or two is Calibre's call rather than Bindery's, and it turns on which route arrives first. Bindery runs `calibredb add` without `--duplicates`, so Calibre itself skips a file whose title and author it already holds, and CWA applies its own rules to what it ingests. Where the two routes disagree about the title or the author, say Bindery's canonical metadata against the file's embedded tags, nothing matches and you get two rows. Either way, pick one route and turn the other off.

**`calibredb unreachable` or `binary_path ... not found`.** The official image does not ship Calibre. Bind mount a Calibre install and set **Binary path (optional)**, or switch to the Bridge plugin, which needs no Calibre binary on Bindery's side.

**Books land in the drop folder and stay there.** The external tool is not watching that folder, or has no permission to delete from it. That is a tool side problem; Bindery has done its part once the file is in the folder.

**Audiobooks never reach CWA.** Expected. The ingest mirror is ebook only. For audiobooks use Audiobookshelf's library scan trigger or the drop folder in External mode.

## See Also

- [`docs/DEPLOYMENT.md`](./DEPLOYMENT.md), section "Handing off to another library tool"
- [`docs/Storage-And-Hardlinks-Wiki.md`](./Storage-And-Hardlinks-Wiki.md) for the import modes
- [`docs/User-Guide-Wiki.md`](./User-Guide-Wiki.md) for the catalogue first model
- [Bindery Bridge plugin](https://github.com/vavallee/bindery-plugins)
