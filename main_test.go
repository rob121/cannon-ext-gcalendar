package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rob121/cannon/extension"
)

func TestCapabilitiesAndMeta(t *testing.T) {
	server := newServer().Handler()

	req := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var caps struct {
		Capabilities map[string]string `json:"capabilities"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&caps); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if caps.Capabilities["page"] != "/page" {
		t.Fatalf("expected page capability, got %#v", caps.Capabilities)
	}
	if caps.Capabilities["configuration"] != "/configuration" {
		t.Fatalf("expected configuration capability, got %#v", caps.Capabilities)
	}
	if caps.Capabilities["templates"] != "/templates" {
		t.Fatalf("expected templates capability, got %#v", caps.Capabilities)
	}
	if caps.Capabilities["help"] != "/help" {
		t.Fatalf("expected help capability, got %#v", caps.Capabilities)
	}

	req = httptest.NewRequest(http.MethodGet, "/meta", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var meta struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Version     string `json:"version"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.Name != extensionName || meta.Title != extensionTitle || meta.Description != extensionDescription || meta.Version != extensionVersion {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	req = httptest.NewRequest(http.MethodGet, "/help", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var help struct {
		Help []string `json:"help"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&help); err != nil {
		t.Fatalf("decode help: %v", err)
	}
	if len(help.Help) != 1 || help.Help[0] != "/help/overview" {
		t.Fatalf("unexpected help list: %#v", help.Help)
	}
}

func TestTemplatesListOverridablePaths(t *testing.T) {
	server := newServer().Handler()
	req := httptest.NewRequest(http.MethodGet, "/templates", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var got extension.TemplateListResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	if len(got.Templates) != 1 {
		t.Fatalf("expected one template, got %#v", got.Templates)
	}
	if got.Templates[0].Path != "calendar/page.html" || got.Templates[0].OverridePath != "extension/calendar/page.html" {
		t.Fatalf("unexpected template path: %#v", got.Templates[0])
	}
}

func TestTemplateDoesNotSetFontFamily(t *testing.T) {
	raw, err := templateFiles.ReadFile("templates/calendar/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "font-family") {
		t.Fatalf("calendar template should inherit the site font without font-family rules")
	}
}

func TestInstallAndConfiguration(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()

	resp := postWire(t, server, "/install", extension.WireRequest{Method: http.MethodPost, URL: "/install", SiteID: "example"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("install failed: %+v", resp)
	}

	db, _, err := extension.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{settingsTableName(), eventsTableName(), syncTableName()} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}

	saveBody := bytes.NewReader([]byte(`{"section":"general","data":{"title":"Events","ics_url":"https://calendar.google.com/calendar/ical/example/basic.ics","refresh_interval_minutes":30,"description_summary_length":180,"login_required_for_details":true}}`))
	req := httptest.NewRequest(http.MethodPost, "/configuration", saveBody)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save configuration: status %d body %s", rec.Code, rec.Body.String())
	}

	settings, err := loadCalendarSettings(settingsTableName())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Title != "Events" || settings.ICSURL == "" || settings.RefreshIntervalMinutes != 30 || settings.DescriptionSummaryLength != 180 || !settings.LoginRequiredForDetails {
		t.Fatalf("unexpected settings: %+v", settings)
	}
}

func TestParseICS(t *testing.T) {
	raw := `BEGIN:VCALENDAR
BEGIN:VEVENT
UID:event-1@example.com
DTSTART;TZID=America/New_York:20260704T090000
DTEND;TZID=America/New_York:20260704T110000
SUMMARY:Independence Day Event
LOCATION:Camp Wilderness
DESCRIPTION:Line one\nLine two\nJoin with Google Meet: https://meet.google.com/abc-defg-hij
END:VEVENT
BEGIN:VEVENT
UID:event-meet@example.com
DTSTART;TZID=America/New_York:20260705T090000
SUMMARY:Meet Event
X-GOOGLE-CONFERENCE:https://meet.google.com/yfa-bmuo-xdg
DESCRIPTION:Public description\n\nJoin with Google Meet: https://meet.google.com/yfa-bmuo-xdg\nOr dial: (US) +1 402-735-0126 PIN: 332516045#\nMore phone numbers: https://tel.meet/yfa-bmuo-xdg?pin=9971233969167&hs=7\n\nLearn more about Meet at: https://support.google.com/a/users/answer/9282720
END:VEVENT
BEGIN:VEVENT
UID:event-2@example.com
DTSTART;VALUE=DATE:20260801
SUMMARY:All Day Event
END:VEVENT
END:VCALENDAR`
	events, err := parseICS(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected three events, got %#v", events)
	}
	if events[0].Summary != "Independence Day Event" || events[0].Location != "Camp Wilderness" {
		t.Fatalf("unexpected first event: %+v", events[0])
	}
	if !strings.Contains(events[0].Description, "Line two") {
		t.Fatalf("expected unescaped description, got %q", events[0].Description)
	}
	if strings.Contains(events[0].Description, "meet.google.com") {
		t.Fatalf("expected meeting link to be removed from public description, got %q", events[0].Description)
	}
	if !strings.Contains(events[0].MeetingDetails, "https://meet.google.com/abc-defg-hij") {
		t.Fatalf("expected meeting details to be extracted, got %q", events[0].MeetingDetails)
	}
	if events[1].Description != "Public description" {
		t.Fatalf("expected conference block removed from public description, got %q", events[1].Description)
	}
	for _, want := range []string{
		"Join with Google Meet: https://meet.google.com/yfa-bmuo-xdg",
		"Or dial: (US) +1 402-735-0126 PIN: 332516045#",
		"More phone numbers: https://tel.meet/yfa-bmuo-xdg?pin=9971233969167&hs=7",
		"Learn more about Meet at: https://support.google.com/a/users/answer/9282720",
	} {
		if !strings.Contains(events[1].MeetingDetails, want) {
			t.Fatalf("expected conference details to contain %q, got %q", want, events[1].MeetingDetails)
		}
	}
	if !events[2].AllDay {
		t.Fatalf("expected all day event: %+v", events[2])
	}
}

func TestDescriptionHTMLIsSanitizedAndCapped(t *testing.T) {
	got := string(renderDescriptionHTML(`<p>Training <strong>Academy</strong> <a href="https://example.com/path?x=1&amp;y=2" onclick="bad()">details</a><script>alert(1)</script></p>`, 120))
	for _, want := range []string{`<p>`, `<strong>Academy</strong>`, `href="https://example.com/path?x=1&amp;y=2"`, `details</a>`, `</p>`} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered description to contain %q, got %q", want, got)
		}
	}
	for _, reject := range []string{"onclick", "<script", "alert(1)"} {
		if strings.Contains(got, reject) {
			t.Fatalf("expected rendered description to strip %q, got %q", reject, got)
		}
	}

	capped := string(renderDescriptionHTML(`<p>1234567890abcdef</p>`, 10))
	if capped != "<p>1234567890...</p>" {
		t.Fatalf("expected capped description, got %q", capped)
	}
}

func TestPageRendersCalendarViewsFromCache(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()
	postWire(t, server, "/install", extension.WireRequest{Method: http.MethodPost, URL: "/install", SiteID: "example"})

	db, _, err := extension.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	store := extension.DBConfigurationStore(db, settingsTableName())
	if err := store.Save(configSection, map[string]any{
		"title":                      "Ranger Events",
		"ics_url":                    "https://calendar.google.com/calendar/ical/example/basic.ics",
		"refresh_interval_minutes":   1440,
		"description_summary_length": 80,
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 4, 9, 0, 0, 0, time.Local)
	end := start.Add(2 * time.Hour)
	event := calendarEvent{
		EventID:     "event-1",
		UID:         "event-1@example.com",
		Summary:     "Section Picnic",
		Description: `<p>Bring a chair and <strong>water</strong>.</p><script>alert("bad")</script>`,
		Location:    "Camp Wilderness",
		StartAt:     start,
		EndAt:       &end,
	}
	if err := db.Table(eventsTableName()).Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(syncTableName()).Save(&calendarSync{Key: "default", LastFetchedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	resp := postWire(t, server, "/page/calendar", extension.WireRequest{
		Method:   http.MethodGet,
		URL:      "/calendar?view=calendar&month=2026-07",
		SiteID:   "example",
		PageItem: "calendar",
		PageData: map[string]any{"default_view": "calendar"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, resp.StatusCode, resp.Body)
	}
	for _, want := range []string{"Section Picnic", "Ranger Events", `data-calendar-view="calendar"`, "Month List", "Year List", "Day"} {
		if !strings.Contains(resp.Body, want) {
			t.Fatalf("expected body to contain %q, got %q", want, resp.Body)
		}
	}

	resp = postWire(t, server, "/page/calendar", extension.WireRequest{
		Method:   http.MethodGet,
		URL:      "/calendar?view=day&date=2026-07-04",
		SiteID:   "example",
		PageItem: "calendar",
	})
	if !strings.Contains(resp.Body, "Camp Wilderness") {
		t.Fatalf("expected day view event location, got %q", resp.Body)
	}
	for _, want := range []string{`<p>Bring a chair and <strong>water</strong>.</p>`, "gcal-event-description"} {
		if !strings.Contains(resp.Body, want) {
			t.Fatalf("expected rendered HTML description to contain %q, got %q", want, resp.Body)
		}
	}
	if strings.Contains(resp.Body, "&lt;p&gt;") || strings.Contains(resp.Body, "<script") {
		t.Fatalf("expected description HTML to render safely, got %q", resp.Body)
	}

	resp = postWire(t, server, "/page/calendar", extension.WireRequest{
		Method:   http.MethodGet,
		URL:      "/calendar?view=year&year=2026",
		SiteID:   "example",
		PageItem: "calendar",
	})
	if !strings.Contains(resp.Body, `href="/calendar?date=2026-07-04&amp;view=day"`) {
		t.Fatalf("expected year view event to link to day detail view, got %q", resp.Body)
	}
}

func TestPageCanRequireLoginForMeetingDetails(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()
	postWire(t, server, "/install", extension.WireRequest{Method: http.MethodPost, URL: "/install", SiteID: "example"})

	db, _, err := extension.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	store := extension.DBConfigurationStore(db, settingsTableName())
	if err := store.Save(configSection, map[string]any{
		"ics_url":                    "https://calendar.google.com/calendar/ical/example/basic.ics",
		"refresh_interval_minutes":   1440,
		"description_summary_length": 80,
		"login_required_for_details": true,
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 4, 9, 0, 0, 0, time.Local)
	event := calendarEvent{
		EventID:     "event-private",
		UID:         "event-private@example.com",
		Summary:     "Private Meeting",
		Description: `<p>Private agenda.</p>`,
		MeetingDetails: `Join with Google Meet
https://meet.google.com/abc-defg-hij`,
		Location: "Members Room",
		StartAt:  start,
	}
	if err := db.Table(eventsTableName()).Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(syncTableName()).Save(&calendarSync{Key: "default", LastFetchedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	resp := postWire(t, server, "/page/calendar", extension.WireRequest{
		Method:   http.MethodGet,
		URL:      "/calendar?view=day&date=2026-07-04",
		SiteID:   "example",
		PageItem: "calendar",
	})
	if !strings.Contains(resp.Body, "Members Room") || !strings.Contains(resp.Body, "<p>Private agenda.</p>") {
		t.Fatalf("expected anonymous render to keep location and description public, got %q", resp.Body)
	}
	if strings.Contains(resp.Body, "meet.google.com") || strings.Contains(resp.Body, "Join with Google Meet") {
		t.Fatalf("expected anonymous render to hide meeting join details, got %q", resp.Body)
	}
	if !strings.Contains(resp.Body, "Sign in to view meeting details.") {
		t.Fatalf("expected login required message, got %q", resp.Body)
	}

	resp = postWire(t, server, "/page/calendar", extension.WireRequest{
		Method:   http.MethodGet,
		URL:      "/calendar?view=day&date=2026-07-04",
		SiteID:   "example",
		PageItem: "calendar",
		User:     map[string]any{"authenticated": true},
	})
	if !strings.Contains(resp.Body, "Members Room") || !strings.Contains(resp.Body, "<p>Private agenda.</p>") || !strings.Contains(resp.Body, "meet.google.com/abc-defg-hij") {
		t.Fatalf("expected authenticated render to show meeting details, got %q", resp.Body)
	}
}

func TestPageMigratesExistingEventsTable(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()

	db, _, err := extension.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := extension.MigrateConfigurationStore(db, settingsTableName()); err != nil {
		t.Fatal(err)
	}
	if err := db.Table(eventsTableName()).AutoMigrate(&legacyCalendarEvent{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Table(syncTableName()).AutoMigrate(&calendarSync{}); err != nil {
		t.Fatal(err)
	}
	store := extension.DBConfigurationStore(db, settingsTableName())
	if err := store.Save(configSection, map[string]any{
		"ics_url":                  "https://calendar.google.com/calendar/ical/example/basic.ics",
		"refresh_interval_minutes": 1440,
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 4, 9, 0, 0, 0, time.Local)
	if err := db.Table(eventsTableName()).Create(&legacyCalendarEvent{
		EventID:  "legacy-event",
		UID:      "legacy-event@example.com",
		Summary:  "Legacy Event",
		StartAt:  start,
		Location: "Legacy Room",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(syncTableName()).Save(&calendarSync{Key: "default", LastFetchedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn(eventsTableName(), "meeting_details") {
		t.Fatal("legacy table unexpectedly has meeting_details before render")
	}

	resp := postWire(t, server, "/page/calendar", extension.WireRequest{
		Method:   http.MethodGet,
		URL:      "/calendar?view=day&date=2026-07-04",
		SiteID:   "example",
		PageItem: "calendar",
	})
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Body, "Legacy Event") {
		t.Fatalf("expected migrated page render, got %+v", resp)
	}
	if !db.Migrator().HasColumn(eventsTableName(), "meeting_details") {
		t.Fatal("expected page render to migrate meeting_details column")
	}
}

type legacyCalendarEvent struct {
	EventID     string     `gorm:"primaryKey;column:event_id;size:96"`
	UID         string     `gorm:"column:uid;size:512;index"`
	Summary     string     `gorm:"column:summary;size:512;not null"`
	Description string     `gorm:"column:description;type:text"`
	Location    string     `gorm:"column:location;size:512"`
	StartAt     time.Time  `gorm:"column:start_at;index;not null"`
	EndAt       *time.Time `gorm:"column:end_at;index"`
	AllDay      bool       `gorm:"column:all_day;not null;default:false"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
}

func setupExtensionDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sites.json")
	dbPath := filepath.Join(dir, "example.sqlite")
	raw := fmt.Sprintf(`{
  "install_enabled": false,
  "data_root": %q,
  "sites": [{
    "id": "example",
    "name": "Example",
    "host": "http://localhost:8001",
    "database": {"type": "sqlite", "dsn": %q},
    "template_dir": %q,
    "assets_dir": %q,
    "tmp_dir": %q,
    "language_dir": %q
  }]
}`, dir, dbPath, filepath.Join(dir, "templates"), filepath.Join(dir, "assets"), filepath.Join(dir, "tmp"), filepath.Join(dir, "language"))
	if err := os.WriteFile(cfgPath, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANNON_CONFIG", cfgPath)
	oldArgs := os.Args
	os.Args = []string{"gcalendar-extension", "--site=example", "--socket=/tmp/gcalendar.sock"}
	t.Cleanup(func() { os.Args = oldArgs })
}

func postWire(t *testing.T, handler http.Handler, path string, wire extension.WireRequest) extension.WireResponse {
	t.Helper()
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp extension.WireResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode wire response: %v", err)
	}
	return resp
}

func settingsTableName() string {
	return extension.TableName(extension.Info{Name: extensionName}.TablePrefix(), "settings")
}

func eventsTableName() string {
	return extension.TableName(extension.Info{Name: extensionName}.TablePrefix(), "events")
}

func syncTableName() string {
	return extension.TableName(extension.Info{Name: extensionName}.TablePrefix(), "sync")
}
