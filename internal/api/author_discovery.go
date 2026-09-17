package api

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// ErrAuthorSyncRunning is returned by DiscoverAuthorBooks when a catalogue
// sync for the same author is already in flight, typically a manual Refresh
// the user clicked while the scheduled pass reached that author. Discovery
// skips the author instead of running a second sync beside the first: two
// concurrent runs race each other's creates and would announce the same book
// twice.
var ErrAuthorSyncRunning = errors.New("a catalogue sync for this author is already running")

// bookAnnouncedListLimit caps how many books one bookAnnounced payload lists.
// The rest are counted in "more", so a relink that adds a whole catalogue
// still sends one readable message.
const bookAnnouncedListLimit = 10

// Length caps for provider text in the bookAnnounced payload. OpenLibrary is
// publicly editable, so a title is whatever someone typed there, and it goes
// straight into an admin's chat channel.
const (
	announceMaxTitleRunes     = 200
	announceMaxAuthorRunes    = 200
	announceMaxForeignIDRunes = 100
)

// eventSender is the part of the notifier the author handler uses. An
// interface so tests can record events without a webhook server.
type eventSender interface {
	Send(ctx context.Context, eventType string, payload map[string]interface{})
}

// WithNotifier attaches the webhook notifier so catalogue syncs can publish
// bookAnnounced (#2236). Without it nothing is announced.
func (h *AuthorHandler) WithNotifier(n eventSender) *AuthorHandler {
	h.notif = n
	return h
}

// DiscoverAuthorBooks runs one unattended discovery sync for author and
// reports how many books it created, along with the provider error when the
// author's works could not be fetched (the scheduler inspects it for a rate
// limit and backs off).
//
// It is the refresh path in every respect but two: it never searches or grabs
// (a created book reaches an indexer only through the existing search-wanted
// sweep and its auto grab switch), and it refuses to start while another sync
// for the same author is running, returning ErrAuthorSyncRunning. It runs
// synchronously on the caller's goroutine.
func (h *AuthorHandler) DiscoverAuthorBooks(ctx context.Context, author *models.Author) (int, error) {
	if author == nil || author.ID == 0 {
		return 0, errors.New("discover author books: author has no id")
	}
	if !h.runningSyncs.tryStart(author.ID) {
		return 0, ErrAuthorSyncRunning
	}
	snapshot := *author
	return h.runCatalogueSync(ctx, &snapshot, catalogueSyncOptions{
		mediaType:   h.resolveDefaultMediaType(ctx),
		discovery:   true,
		syncClaimed: true,
	})
}

// catalogueWasPopulated captures, before a sync creates anything, whether the
// author already had a catalogue. It is the baseline the announce rule needs:
// the first population of an author lists their whole bibliography, which is
// not news. Only discovery runs need the answer, so every other run skips the
// read and reports false.
//
// A read error counts as populated (authorAwaitsFirstCatalogue answers false),
// so the worst case is one announcement too many, never a silent new book.
func (h *AuthorHandler) catalogueWasPopulated(ctx context.Context, author *models.Author, opts catalogueSyncOptions, bookCount int) bool {
	if !opts.discovery || opts.onlyForeignID != "" {
		return false
	}
	return !h.authorAwaitsFirstCatalogue(ctx, author, bookCount)
}

// shouldAnnounceDiscovered is the whole bookAnnounced rule, in one place.
//
// A run announces only when it is a discovery run (a manual Refresh, bulk
// refresh, Refresh all, relink or the scheduled discovery job), the author's
// catalogue was populated before the run started, and the run created at
// least one book. The add flow and AddBook's single work fallback are not
// discovery runs, and the first population of an author is not a populated
// catalogue, so none of those ever announce.
func shouldAnnounceDiscovered(opts catalogueSyncOptions, populatedBefore bool, created int) bool {
	return opts.discovery && opts.onlyForeignID == "" && populatedBefore && created > 0
}

// announceDiscoveredBooks sends one bookAnnounced event for a finished sync
// when shouldAnnounceDiscovered says so. It is called once per author run,
// from the end of runCatalogueSync.
func (h *AuthorHandler) announceDiscoveredBooks(ctx context.Context, author *models.Author, opts catalogueSyncOptions, populatedBefore bool, created []models.Book) {
	if h.notif == nil || author == nil || !shouldAnnounceDiscovered(opts, populatedBefore, len(created)) {
		return
	}
	h.notif.Send(ctx, notifier.EventBookAnnounced, bookAnnouncedPayload(author, created))
}

// bookAnnouncedPayload builds the event payload: the author, the total count,
// up to bookAnnouncedListLimit books with each one's monitored flag, and a
// "more" count for the rest. Every provider supplied string is capped and
// stripped of control characters (S3).
func bookAnnouncedPayload(author *models.Author, created []models.Book) map[string]interface{} {
	listed := created
	if len(listed) > bookAnnouncedListLimit {
		listed = listed[:bookAnnouncedListLimit]
	}
	books := make([]map[string]interface{}, 0, len(listed))
	titles := make([]string, 0, len(listed))
	for i := range listed {
		b := &listed[i]
		title := announceText(b.Title, announceMaxTitleRunes)
		entry := map[string]interface{}{
			"id":        b.ID,
			"title":     title,
			"foreignId": announceText(b.ForeignID, announceMaxForeignIDRunes),
			"monitored": b.Monitored,
		}
		if b.ReleaseDate != nil {
			entry["releaseDate"] = b.ReleaseDate.UTC().Format("2006-01-02")
		}
		books = append(books, entry)
		titles = append(titles, title)
	}
	more := len(created) - len(listed)
	message := strings.Join(titles, ", ")
	if more > 0 {
		message += " and " + strconv.Itoa(more) + " more"
	}
	return map[string]interface{}{
		"author":   announceText(author.Name, announceMaxAuthorRunes),
		"authorId": author.ID,
		"count":    len(created),
		"books":    books,
		"more":     more,
		"message":  message,
	}
}

// announceText makes provider text safe to put in a webhook payload: control
// and bidirectional override characters become spaces, runs of whitespace
// collapse to one space so a title cannot break a chat message into lines,
// and the result is cut to maxRunes with an ellipsis.
func announceText(s string, maxRunes int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, s)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if maxRunes > 0 && utf8.RuneCountInString(cleaned) > maxRunes {
		runes := []rune(cleaned)
		cleaned = strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
	}
	return cleaned
}

// editionPrefetchCandidates narrows the MinPages and SkipMissingISBN edition
// prefetch to works the author does not already have (P1). The create loop
// exempts a work that resolves to an existing book from both filters, so the
// editions fetched for it were never read: on a 65 book author that was 65
// provider calls on every refresh, and on every scheduled discovery pass.
//
// A work is known when its foreign id or its Hardcover id is the foreign id of
// one of the author's loaded books (excluded ones included) or one of the
// identifiers recorded against them. Each of those ids makes
// resolveExistingBook return a row, so skipping the fetch changes no outcome.
// An identifier read failure falls back to the loaded foreign ids alone, which
// only means fetching a few editions that were not needed.
func (h *AuthorHandler) editionPrefetchCandidates(ctx context.Context, authorID int64, known, candidates []models.Book) []models.Book {
	ids := make(map[string]struct{}, len(known))
	for i := range known {
		if id := strings.TrimSpace(known[i].ForeignID); id != "" {
			ids[id] = struct{}{}
		}
	}
	if h.books != nil && authorID != 0 {
		if byBook, err := h.books.ListBookIdentifiersByAuthor(ctx, authorID); err == nil {
			for _, identifiers := range byBook {
				for _, identifier := range identifiers {
					if id := strings.TrimSpace(identifier.ForeignID); id != "" {
						ids[id] = struct{}{}
					}
				}
			}
		}
	}
	isKnown := func(id string) bool {
		id = strings.TrimSpace(id)
		if id == "" {
			return false
		}
		_, ok := ids[id]
		return ok
	}
	out := make([]models.Book, 0, len(candidates))
	for _, b := range candidates {
		if isKnown(b.ForeignID) || isKnown(b.HardcoverForeignID) {
			continue
		}
		out = append(out, b)
	}
	return out
}
