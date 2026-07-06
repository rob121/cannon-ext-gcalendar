# Cannon Google Calendar Extension

Renders events from a public Google Calendar ICS feed.

## Configuration

Use the extension configuration screen to set:

- `title`: heading shown above the calendar. Defaults to `Calendar`.
- `ics_url`: public Google Calendar `.ics` feed URL.
- `refresh_interval_minutes`: how often the extension should refresh and cache events.
- `description_summary_length`: maximum text characters to show from event descriptions. Set to `0` to show the full description.
- `login_required_for_details`: require a signed-in Cannon user before showing online meeting join details such as Google Meet links.

## Page

Register the `Google Calendar` page in Cannon routes. The route supports these query-string views:

- `?view=calendar&month=2026-07`
- `?view=month&month=2026-07`
- `?view=year&year=2026`
- `?view=day&date=2026-07-04`

The route metadata field `default_view` controls the initial view when no `view` query parameter is present.
