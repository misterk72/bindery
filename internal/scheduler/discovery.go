package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// Unattended release discovery (#2236).
//
// Before this job nothing scheduled ever created a book row: refresh-metadata
// copies four profile fields and search-wanted only walks books that already
// exist, so a followed author's new release reached the library only when a
// person clicked Refresh. The discovery job runs the same catalogue sync a
// manual Refresh runs, a few authors at a time, so every eligible author is
// checked once per configured interval.

// settingAuthorDiscoveryInterval mirrors api.SettingAuthorDiscoveryInterval.
// The scheduler does not import internal/api, so the key is repeated here.
const settingAuthorDiscoveryInterval = "authors.discovery.interval"

// Bounds and default of the discovery interval, mirroring the API validator
// for authors.discovery.interval. The default is weekly: a new book is rarely
// urgent, and a week keeps provider traffic to one works call per author per
// week.
const (
	defaultDiscoveryInterval = 168 * time.Hour
	minDiscoveryInterval     = 24 * time.Hour
	maxDiscoveryInterval     = 720 * time.Hour
)

// maxDiscoveryBatch caps how many authors one hourly tick checks. With the
// pace below a full batch takes a little over a minute of sleeps plus the
// syncs themselves, well inside the hour, and a tick that does overrun is
// skipped by the cron chain's SkipIfStillRunning rather than queued (P8).
const maxDiscoveryBatch = 25

// discoveryPace is the gap between two authors in one tick, so a batch does
// not burst the metadata provider. A var only so tests can set it to 0.
var discoveryPace = 3 * time.Second

// DiscoveryOutcome is what one author's discovery run reports back to the
// job. The scheduler cannot import the api or hardcover packages, so the
// wiring in main.go translates their errors into these flags.
type DiscoveryOutcome struct {
	// Created is how many books the run added.
	Created int
	// Err is the run's error, if any. An ordinary error still stamps the
	// author as checked, so one broken author cannot pin the front of the
	// queue and starve everyone behind it.
	Err error
	// Backoff means the metadata provider refused to answer (a rate limit).
	// The pass stops at once and the author is not stamped, so it is first
	// in line on the next tick.
	Backoff bool
	// Busy means another catalogue sync for this author was already running,
	// typically a manual Refresh. The author is skipped and not stamped.
	Busy bool
}

// AuthorDiscoverer runs discovery for one author and says whether a bulk
// refresh, which already walks every author, is in progress.
type AuthorDiscoverer interface {
	DiscoverAuthor(ctx context.Context, author *models.Author) DiscoveryOutcome
	BulkRefreshRunning() bool
}

// AuthorDiscovererFuncs adapts two closures to AuthorDiscoverer. A nil
// BulkRunning reports that no bulk refresh is running.
type AuthorDiscovererFuncs struct {
	Discover    func(ctx context.Context, author *models.Author) DiscoveryOutcome
	BulkRunning func() bool
}

// DiscoverAuthor implements AuthorDiscoverer.
func (f AuthorDiscovererFuncs) DiscoverAuthor(ctx context.Context, author *models.Author) DiscoveryOutcome {
	return f.Discover(ctx, author)
}

// BulkRefreshRunning implements AuthorDiscoverer.
func (f AuthorDiscovererFuncs) BulkRefreshRunning() bool {
	return f.BulkRunning != nil && f.BulkRunning()
}

// discoveryAuthors is the part of the author repo the job reads and writes.
type discoveryAuthors interface {
	CountDiscoveryEligible(ctx context.Context) (int, error)
	ListDiscoveryDue(ctx context.Context, cutoff time.Time, limit int) ([]models.Author, error)
	StampDiscovery(ctx context.Context, authorID int64, when time.Time) error
}

// discoveryJob holds the job's collaborators. now is injected so due date
// logic is tested without sleeping (C8).
type discoveryJob struct {
	authors    discoveryAuthors
	discoverer AuthorDiscoverer
	// interval returns the configured interval, or ok=false when discovery
	// is off. Read on every tick, so a settings change needs no restart.
	interval func() (d time.Duration, ok bool)
	now      func() time.Time
}

// discoveryTickResult summarises one tick for the log line and for tests.
type discoveryTickResult struct {
	Skipped string // why the tick did nothing, empty when it ran
	Checked int
	Created int
	Backoff bool
}

// WithAuthorDiscoverer registers the hourly author-discovery job. A nil
// discoverer registers nothing. It may be called before or after Start: the
// job is added to the cron directly, because the discoverer (the author
// handler) is built after the scheduler has started.
func (s *Scheduler) WithAuthorDiscoverer(d AuthorDiscoverer) {
	if d == nil || s.authors == nil {
		return
	}
	job := &discoveryJob{
		authors:    s.authors,
		discoverer: d,
		interval:   s.resolveDiscoveryInterval,
		now:        time.Now,
	}
	s.cron.AddFunc("@every 1h", runJob("author-discovery", func() {
		job.tick(s.ctx())
	}))
}

// resolveDiscoveryInterval reads authors.discovery.interval. "off" disables
// the job; anything unset, unparseable or out of bounds falls back to the
// weekly default, the same rules as every other interval setting.
func (s *Scheduler) resolveDiscoveryInterval() (time.Duration, bool) {
	if s.settings != nil {
		if v, _ := s.settings.Get(s.ctx(), settingAuthorDiscoveryInterval); v != nil && v.Value == "off" {
			return 0, false
		}
	}
	return s.resolveInterval(settingAuthorDiscoveryInterval, defaultDiscoveryInterval, minDiscoveryInterval, maxDiscoveryInterval), true
}

// discoveryBatchSize spreads eligible authors over the hours in interval, so
// each is checked about once per interval: ceil(eligible / hours), at least
// one and at most maxDiscoveryBatch. Past 25 authors per hour the interval
// stretches instead of the batch growing.
func discoveryBatchSize(eligible int, interval time.Duration) int {
	hours := int(interval / time.Hour)
	if hours < 1 {
		hours = 1
	}
	batch := (eligible + hours - 1) / hours
	if batch < 1 {
		batch = 1
	}
	if batch > maxDiscoveryBatch {
		batch = maxDiscoveryBatch
	}
	return batch
}

// tick runs one discovery pass: pick the batch of authors whose last check
// is older than the interval and sync them one at a time.
func (j *discoveryJob) tick(ctx context.Context) discoveryTickResult {
	start := j.now()
	var res discoveryTickResult
	interval, on := j.interval()
	if !on {
		res.Skipped = "off"
		slog.Debug("job: author discovery is off")
		return res
	}
	// A bulk refresh already runs the same sync over every author. Running
	// beside it doubles the provider traffic for nothing.
	if j.discoverer.BulkRefreshRunning() {
		res.Skipped = "bulk refresh running"
		slog.Info("job: author discovery skipped, a bulk author refresh is running")
		return res
	}
	eligible, err := j.authors.CountDiscoveryEligible(ctx)
	if err != nil {
		slog.Warn("job: author discovery could not count authors", "error", err)
		res.Skipped = "count failed"
		return res
	}
	if eligible == 0 {
		res.Skipped = "no eligible authors"
		return res
	}
	due, err := j.authors.ListDiscoveryDue(ctx, start.Add(-interval), discoveryBatchSize(eligible, interval))
	if err != nil {
		slog.Warn("job: author discovery could not list due authors", "error", err)
		res.Skipped = "list failed"
		return res
	}

	for i := range due {
		if i > 0 && !sleepCtx(ctx, discoveryPace) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		author := due[i]
		out := j.discoverer.DiscoverAuthor(ctx, &author)
		if out.Backoff {
			res.Backoff = true
			slog.Warn("job: author discovery stopped, the metadata provider is rate limiting",
				"author", author.Name, "checked", res.Checked, "error", out.Err)
			break
		}
		if out.Busy {
			slog.Debug("job: author discovery skipped an author whose sync is already running", "author", author.Name)
			continue
		}
		if out.Err != nil {
			slog.Warn("job: author discovery failed for an author; will retry next interval",
				"author", author.Name, "error", out.Err)
		}
		res.Checked++
		res.Created += out.Created
		if err := j.authors.StampDiscovery(ctx, author.ID, j.now()); err != nil {
			slog.Warn("job: author discovery could not record the check", "author", author.Name, "error", err)
		}
	}

	slog.Info("job: author discovery pass finished",
		"eligible", eligible, "due", len(due), "checked", res.Checked, "created", res.Created,
		"backoff", res.Backoff, "elapsed", j.now().Sub(start).Round(time.Millisecond))
	return res
}

// sleepCtx waits d or until ctx is done, reporting whether the full wait
// elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
