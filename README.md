# Cannon Google Calendar Extension

Renders events from a public Google Calendar ICS feed inside Cannon.

## Features

- Pulls and caches events from a configured `.ics` URL.
- Configurable refresh interval.
- Frontend calendar title setting.
- Multiple public views: year list, month list, calendar grid, and day view.
- Optional description summary length.
- Extracts Google Meet details from event descriptions when Google conference metadata is present.
- Optional login requirement for meeting details while keeping event descriptions public.
- Overridable built-in templates.

## Cannon Capabilities

This extension exposes:

- `/meta`: extension name, description, version, and update URL base.
- `/capabilities`: page, admin, configuration, help, and templates capabilities.
- `/admin`: calendar status and management UI.
- `/configuration`: ICS URL, refresh interval, title, summary length, and meeting detail settings.
- `/page`: calendar page definitions and rendering.
- `/templates`: built-in template metadata for overrides.
- `/help`: extension help content.
- `/install`: creates the extension tables.

## Configuration

Use Cannon's extension configuration screen to set:

- `title`: heading shown on the calendar page.
- `ics_url`: public Google Calendar ICS feed URL.
- `refresh_interval_minutes`: how often events are refreshed.
- `description_summary_length`: maximum description characters to show; use `0` for full descriptions.
- `login_required_for_details`: require login before showing Google Meet or dial-in details.

## Usage

1. Install and activate `cannon-ext-gcalendar` in Cannon.
2. Configure the public Google Calendar ICS URL.
3. Add a Cannon route using the Google Calendar page type.
4. Pick a default view in the route metadata when needed.

Supported query examples:

- `?view=calendar&month=2026-07`
- `?view=month&month=2026-07`
- `?view=year&year=2026`
- `?view=day&date=2026-07-04`

## Releases

The extension reports:

- Name: `cannon-ext-gcalendar`
- Version: `0.1.0`
- Update URL base: `https://github.com/rob121/cannon-ext-gcalendar/releases/download`

GitHub releases should include a `cannon-extension.json` manifest plus platform binaries.
