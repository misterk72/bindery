package notifier

import (
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// bookAnnounced is opt in per notification (#2236): a webhook created before
// the event existed, with every other toggle on, must not start receiving it.
func TestMatchesEvent_BookAnnouncedIsItsOwnToggle(t *testing.T) {
	n := &Notifier{}
	everythingElse := models.Notification{OnGrab: true, OnImport: true, OnUpgrade: true, OnFailure: true, OnHealth: true}
	if n.matchesEvent(&everythingElse, EventBookAnnounced) {
		t.Error("bookAnnounced matched a notification that never opted into it")
	}
	optedIn := models.Notification{OnBookAnnounced: true}
	if !n.matchesEvent(&optedIn, EventBookAnnounced) {
		t.Error("bookAnnounced did not match a notification with OnBookAnnounced set")
	}
	if n.matchesEvent(&optedIn, EventGrabbed) {
		t.Error("OnBookAnnounced alone must not match other events")
	}
}

func TestNormalizeEventPayload_BookAnnounced(t *testing.T) {
	cases := []struct {
		name      string
		payload   map[string]interface{}
		wantTitle string
		wantMsg   string
	}{
		{
			name:      "several books",
			payload:   map[string]interface{}{"author": "Ann Leckie", "count": 3, "message": "A, B and 1 more"},
			wantTitle: "New Books Found",
			wantMsg:   "Ann Leckie: A, B and 1 more",
		},
		{
			name:      "one book",
			payload:   map[string]interface{}{"author": "Ann Leckie", "count": 1, "message": "Translation State"},
			wantTitle: "New Book Found",
			wantMsg:   "Ann Leckie: Translation State",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeEventPayload(EventBookAnnounced, tc.payload)
			if out["title"] != tc.wantTitle {
				t.Errorf("title = %v, want %q", out["title"], tc.wantTitle)
			}
			if out["message"] != tc.wantMsg {
				t.Errorf("message = %v, want %q", out["message"], tc.wantMsg)
			}
			if out["eventType"] != EventBookAnnounced {
				t.Errorf("eventType = %v, want %q", out["eventType"], EventBookAnnounced)
			}
		})
	}
}
