package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// worksMetaProvider is mockMetaProvider plus an author works list, which is
// what a catalogue sync reads.
type worksMetaProvider struct {
	mockMetaProvider
	works []models.Book
}

func (p *worksMetaProvider) GetAuthorWorks(context.Context, string) ([]models.Book, error) {
	return p.works, nil
}

// TestScheduledJobs_MonitoredAuthorGainsNewUpstreamWork is the #2236 report
// as a test: a monitored author already in the library publishes a new work
// upstream, the scheduler's jobs run, and the new book must exist afterwards.
// On main no scheduled job creates a book row, so the book never appears.
//
// Discovery ships off, so the test stores the Weekly interval first, which is
// what an operator picks in Settings, General, New release discovery. That it
// has to be stored at all is the point of
// TestDiscovery_OffUntilAnIntervalIsStored; here it is only the precondition.
func TestScheduledJobs_MonitoredAuthorGainsNewUpstreamWork(t *testing.T) {
	prevPace := discoveryPace
	discoveryPace = 0
	defer func() { discoveryPace = prevPace }()

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	settings := db.NewSettingsRepo(database)
	profiles := db.NewMetadataProfileRepo(database)

	author := &models.Author{
		ForeignID: "OL2236A", Name: "Ann Leckie", SortName: "Leckie, Ann",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := authors.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	existing := models.Book{
		ForeignID: "OL2236W0", AuthorID: author.ID, Title: "Ancillary Justice", SortTitle: "ancillary justice",
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := books.Create(ctx, &existing); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settingAuthorDiscoveryInterval, "168h"); err != nil {
		t.Fatal(err)
	}

	provider := &worksMetaProvider{
		mockMetaProvider: mockMetaProvider{author: &models.Author{ForeignID: "OL2236A", Name: "Ann Leckie"}},
		works: []models.Book{
			{ForeignID: "OL2236W0", Title: "Ancillary Justice", SortTitle: "ancillary justice", Language: "eng",
				MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
			{ForeignID: "OL2236W1", Title: "Translation State", SortTitle: "translation state", Language: "eng",
				MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary"},
		},
	}
	agg := metadata.NewAggregator(provider)
	handler := api.NewAuthorHandler(authors, nil, books, nil, agg, settings, profiles, nil)

	s := &Scheduler{
		cron:     cron.New(cron.WithSeconds()),
		meta:     agg,
		authors:  authors,
		books:    books,
		settings: settings,
	}
	s.WithAuthorDiscoverer(AuthorDiscovererFuncs{
		Discover: func(ctx context.Context, a *models.Author) DiscoveryOutcome {
			created, err := handler.DiscoverAuthorBooks(ctx, a)
			return DiscoveryOutcome{Created: created, Err: err}
		},
	})

	// Run the author jobs a scheduler tick would: the metadata refresh that
	// has always existed, then every job registered on the cron.
	s.refreshMetadata()
	for _, e := range s.cron.Entries() {
		e.Job.Run()
	}

	got, err := books.GetByForeignID(ctx, "OL2236W1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("the new upstream work never became a book: no scheduled job discovered it")
	}
	if got.AuthorID != author.ID || !got.Monitored || got.Status != models.BookStatusWanted {
		t.Errorf("discovered book = author %d monitored %v status %q, want author %d, monitored, wanted",
			got.AuthorID, got.Monitored, got.Status, author.ID)
	}
	when, err := authors.LastDiscoveryAt(ctx, author.ID)
	if err != nil || when == nil || time.Since(*when) > time.Minute {
		t.Errorf("discovery cursor after the tick = %v, %v, want a fresh stamp", when, err)
	}
}
