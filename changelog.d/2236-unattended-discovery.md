### Added
- **New releases arrive on their own** (#2236): monitored authors are now checked for new books on a schedule, so a followed author's next book joins your library without clicking Refresh. It is on by default and runs weekly; pick Daily, Monthly or Off in Settings, General, New release discovery. Checks are spread over the week a few authors an hour, pause while you run Refresh all or a bulk refresh, and stop early when a metadata provider rate limits or keeps failing; new books follow each author's monitor mode and metadata profile, and grabbing still waits for the wanted search and auto grab. Authors set to Monitor new items: Don't add them are never checked. Thanks to ianepreston for the detailed report.
- **New book webhook alert** (#2236): a new `bookAnnounced` event lists the books a refresh or scheduled discovery added to an author you already had. It is off for every webhook, existing and new, until you turn on New book in the webhook's triggers.

### Fixed
- If Bindery failed to read an author's books during a refresh, it went on as if the author had none, which could bring back books you had excluded or add a second entry for a book you already had. The refresh now stops instead.

### Changed
- Refreshing an author whose profile filters on page count or ISBN no longer fetches editions for books it already has, so refreshes of large authors make far fewer provider calls.
