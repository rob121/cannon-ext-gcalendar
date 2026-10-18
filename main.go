package main

import (
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	htmlpkg "html"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rob121/cannon/extension"
	"gorm.io/gorm"
)

//go:embed all:templates
var templateFiles embed.FS

//go:embed help/*.md
var helpFiles embed.FS

const (
	extensionName        = "cannon-ext-gcalendar"
	extensionTitle       = "Google Calendar"
	extensionDescription = "Renders public Google Calendar ICS feeds as year, month, calendar grid, and day views with a configurable refresh interval."
	extensionVersion     = "0.2.1"
	updateURLBase        = "https://github.com/rob121/cannon-ext-gcalendar/releases/download"
	defaultAdminMenu     = "Google Calendar"
	defaultTitle         = "Calendar"
	defaultIntervalMin   = 60
	defaultSummaryLength = 500
	configSection        = "general"
)

var calendarConfigSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "title": {
      "type": "string",
      "title": "Title",
      "default": "Calendar",
      "description": "Heading shown above the calendar."
    },
    "ics_url": {
      "type": "string",
      "title": "Calendar ICS URL",
      "format": "uri",
      "description": "Public Google Calendar .ics URL to render."
    },
    "refresh_interval_minutes": {
      "type": "integer",
      "title": "Refresh Interval Minutes",
      "minimum": 1,
      "default": 60,
      "description": "How often Cannon should refresh the ICS feed."
    },
    "description_summary_length": {
      "type": "integer",
      "title": "Description Summary Length",
      "minimum": 0,
      "default": 500,
      "description": "Maximum number of text characters to show from event descriptions. Use 0 to show the full description."
    },
    "login_required_for_details": {
      "type": "boolean",
      "title": "Login Required For Meeting Details",
      "default": false,
      "description": "Require a signed-in Cannon user before showing online meeting join details such as Google Meet links."
    }
  },
  "required": ["ics_url"]
}`)

var calendarConfigUI = json.RawMessage(`{
  "type": "VerticalLayout",
  "elements": [
    {"type": "Control", "scope": "#/properties/title"},
    {"type": "Control", "scope": "#/properties/ics_url"},
    {"type": "Control", "scope": "#/properties/refresh_interval_minutes"},
    {"type": "Control", "scope": "#/properties/description_summary_length"},
    {"type": "Control", "scope": "#/properties/login_required_for_details"}
  ]
}`)

type calendarApp struct {
	info          extension.Info
	templates     *extension.Templates
	settingsTable string
	schemaOnce    sync.Once
	schemaErr     error
}

type calendarSettings struct {
	Title                    string `json:"title"`
	ICSURL                   string `json:"ics_url"`
	RefreshIntervalMinutes   int    `json:"refresh_interval_minutes"`
	DescriptionSummaryLength int    `json:"description_summary_length"`
	LoginRequiredForDetails  bool   `json:"login_required_for_details"`
}

type calendarEvent struct {
	EventID        string     `gorm:"primaryKey;column:event_id;size:96"`
	UID            string     `gorm:"column:uid;size:512;index"`
	Summary        string     `gorm:"column:summary;size:512;not null"`
	Description    string     `gorm:"column:description;type:text"`
	MeetingDetails string     `gorm:"column:meeting_details;type:text"`
	Location       string     `gorm:"column:location;size:512"`
	StartAt        time.Time  `gorm:"column:start_at;index;not null"`
	EndAt          *time.Time `gorm:"column:end_at;index"`
	AllDay         bool       `gorm:"column:all_day;not null;default:false"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}

type calendarSync struct {
	Key           string    `gorm:"primaryKey;column:key;size:64"`
	LastFetchedAt time.Time `gorm:"column:last_fetched_at"`
	LastError     string    `gorm:"column:last_error;type:text"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

type calendarViewLink struct {
	View   string
	Label  string
	URL    string
	Active bool
}

type calendarDay struct {
	Date    time.Time
	Day     int
	InMonth bool
	IsToday bool
	Events  []calendarEvent
}

type calendarDisplayEvent struct {
	calendarEvent
	DescriptionHTML      template.HTML
	MeetingDetailsHTML   template.HTML
	DisplayLocation      string
	DetailURL            string
	MeetingDetailsLocked bool
}

func main() {
	if err := newServer().Run(); err != nil {
		log.Fatal(err)
	}
}

func newServer() *extension.Server {
	info := extension.Info{
		Name:          extensionName,
		Title:         extensionTitle,
		Description:   extensionDescription,
		Version:       extensionVersion,
		UpdateURLBase: updateURLBase,
		AdminMenuName: defaultAdminMenu,
	}
	app := &calendarApp{
		info:          info,
		templates:     extension.NewTemplates(templateFiles, "templates"),
		settingsTable: extension.TableName(info.TablePrefix(), "settings"),
	}

	s := extension.New(info)
	s.EmbedTemplates(templateFiles, "templates")
	s.EmbedHelp(helpFiles, "help")
	s.OnInstall(app.install)
	s.OnConfiguration(extension.MapConfiguration([]extension.ConfigurationDefinition{
		{
			ID:       configSection,
			Title:    "Calendar Feed",
			Schema:   calendarConfigSchema,
			UISchema: calendarConfigUI,
		},
	}, configStore{table: app.settingsTable}))
	s.RegisterPage(extension.PageDefinition{
		ID:    "calendar",
		Title: "Google Calendar",
		Fields: []extension.PageField{
			{
				Name:  "default_view",
				Label: "Default View",
				Type:  "select",
				Help:  "Initial view when the route has no view query parameter.",
				Options: []extension.PageFieldOption{
					{Value: "calendar", Label: "Calendar"},
					{Value: "month", Label: "List by Month"},
					{Value: "year", Label: "List by Year"},
					{Value: "day", Label: "Day"},
				},
			},
		},
	}, app.page)

	return s
}

func (app *calendarApp) install(req extension.WireRequest) error {
	return app.ensureSchema()
}

func (app *calendarApp) ensureSchema() error {
	app.schemaOnce.Do(func() {
		app.schemaErr = app.migrateSchema()
	})
	return app.schemaErr
}

func (app *calendarApp) migrateSchema() error {
	db, _, err := extension.OpenDB()
	if err != nil {
		return err
	}
	if err := extension.MigrateConfigurationStore(db, app.settingsTable); err != nil {
		return err
	}
	if err := db.Table(app.eventsTable()).AutoMigrate(&calendarEvent{}); err != nil {
		return err
	}
	return db.Table(app.syncTable()).AutoMigrate(&calendarSync{})
}

func (app *calendarApp) page(item string, req extension.WireRequest) extension.WireResponse {
	if err := app.ensureSchema(); err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	settings, err := loadCalendarSettings(app.settingsTable)
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	if strings.TrimSpace(settings.ICSURL) == "" {
		return app.render("calendar/page.html", map[string]any{
			"Title":       settings.Title,
			"NeedsConfig": true,
			"Message":     "Configure a public Google Calendar ICS URL before rendering events.",
		})
	}

	if err := app.refreshIfDue(settings); err != nil {
		_ = app.recordSyncError(err)
	}

	u, err := parseWireURL(req.URL)
	if err != nil {
		return extension.Error(http.StatusBadRequest, err.Error())
	}
	defaultView := extension.PageDataString(req, "default_view")
	view := normalizeView(firstNonEmpty(u.Query().Get("view"), defaultView, "calendar"))
	activeDate := viewDate(u.Query(), time.Now())
	events, err := app.eventsForView(view, activeDate)
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	syncInfo, _ := app.syncInfo()

	meetingDetailsAllowed := !settings.LoginRequiredForDetails || extension.UserAuthenticated(req)
	return app.render("calendar/page.html", map[string]any{
		"Title":        settings.Title,
		"View":         view,
		"ViewLabel":    viewLabel(view),
		"ActiveDate":   activeDate,
		"ActiveISO":    activeDate.Format("2006-01-02"),
		"Year":         activeDate.Year(),
		"MonthLabel":   activeDate.Format("January 2006"),
		"DayLabel":     activeDate.Format("Monday, January 2, 2006"),
		"Events":       displayEvents(events, settings.DescriptionSummaryLength, u, meetingDetailsAllowed),
		"Weeks":        calendarWeeks(activeDate, events),
		"Views":        viewLinks(u, view, activeDate),
		"PrevURL":      periodURL(u, view, activeDate, -1),
		"NextURL":      periodURL(u, view, activeDate, 1),
		"TodayURL":     setViewDateURL(u, view, time.Now()),
		"LastFetched":  syncInfo.LastFetchedAt,
		"LastError":    syncInfo.LastError,
		"HasFetched":   !syncInfo.LastFetchedAt.IsZero(),
		"Settings":     settings,
		"CalendarItem": item,
	})
}

func (app *calendarApp) refreshIfDue(settings calendarSettings) error {
	syncInfo, err := app.syncInfo()
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	interval := settings.RefreshIntervalMinutes
	if interval <= 0 {
		interval = defaultIntervalMin
	}
	if !syncInfo.LastFetchedAt.IsZero() && time.Since(syncInfo.LastFetchedAt) < time.Duration(interval)*time.Minute {
		return nil
	}
	events, err := fetchICSEvents(settings.ICSURL)
	if err != nil {
		return err
	}
	db, _, err := extension.OpenDB()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(app.eventsTable()).Where("1 = 1").Delete(&calendarEvent{}).Error; err != nil {
			return err
		}
		for _, event := range events {
			if event.EventID == "" {
				event.EventID = eventID(event)
			}
			if err := tx.Table(app.eventsTable()).Save(&event).Error; err != nil {
				return err
			}
		}
		row := calendarSync{Key: "default", LastFetchedAt: time.Now(), LastError: ""}
		return tx.Table(app.syncTable()).Save(&row).Error
	})
}

func (app *calendarApp) recordSyncError(err error) error {
	db, _, openErr := extension.OpenDB()
	if openErr != nil {
		return openErr
	}
	var row calendarSync
	if dbErr := db.Table(app.syncTable()).Where("key = ?", "default").First(&row).Error; dbErr == gorm.ErrRecordNotFound {
		row = calendarSync{Key: "default"}
	} else if dbErr != nil {
		return dbErr
	}
	row.LastError = err.Error()
	return db.Table(app.syncTable()).Save(&row).Error
}

func (app *calendarApp) syncInfo() (calendarSync, error) {
	db, _, err := extension.OpenDB()
	if err != nil {
		return calendarSync{}, err
	}
	var row calendarSync
	err = db.Table(app.syncTable()).Where("key = ?", "default").First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return calendarSync{Key: "default"}, nil
	}
	return row, err
}

func (app *calendarApp) eventsForView(view string, activeDate time.Time) ([]calendarEvent, error) {
	start, end := viewRange(view, activeDate)
	db, _, err := extension.OpenDB()
	if err != nil {
		return nil, err
	}
	var events []calendarEvent
	err = db.Table(app.eventsTable()).
		Where("start_at >= ? AND start_at < ?", start, end).
		Order("start_at asc, summary asc").
		Find(&events).Error
	return events, err
}

func viewRange(view string, activeDate time.Time) (time.Time, time.Time) {
	switch view {
	case "year":
		start := time.Date(activeDate.Year(), 1, 1, 0, 0, 0, 0, activeDate.Location())
		return start, start.AddDate(1, 0, 0)
	case "day":
		start := dayStart(activeDate)
		return start, start.AddDate(0, 0, 1)
	default:
		start := time.Date(activeDate.Year(), activeDate.Month(), 1, 0, 0, 0, 0, activeDate.Location())
		return start, start.AddDate(0, 1, 0)
	}
}

func calendarWeeks(activeDate time.Time, events []calendarEvent) [][]calendarDay {
	monthStart := time.Date(activeDate.Year(), activeDate.Month(), 1, 0, 0, 0, 0, activeDate.Location())
	gridStart := monthStart.AddDate(0, 0, -int(monthStart.Weekday()))
	today := dayStart(time.Now())
	byDay := map[string][]calendarEvent{}
	for _, event := range events {
		key := event.StartAt.Format("2006-01-02")
		byDay[key] = append(byDay[key], event)
	}
	weeks := make([][]calendarDay, 0, 6)
	for week := 0; week < 6; week++ {
		row := make([]calendarDay, 0, 7)
		for day := 0; day < 7; day++ {
			date := gridStart.AddDate(0, 0, week*7+day)
			row = append(row, calendarDay{
				Date:    date,
				Day:     date.Day(),
				InMonth: date.Month() == activeDate.Month(),
				IsToday: dayStart(date).Equal(today),
				Events:  byDay[date.Format("2006-01-02")],
			})
		}
		weeks = append(weeks, row)
	}
	return weeks
}

func fetchICSEvents(rawURL string) ([]calendarEvent, error) {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "webcal://") {
		rawURL = "https://" + strings.TrimPrefix(rawURL, "webcal://")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch ics: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	return parseICS(string(body))
}

func parseICS(raw string) ([]calendarEvent, error) {
	lines := unfoldICSLines(raw)
	var events []calendarEvent
	var current map[string]icsProp
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		switch strings.ToUpper(strings.TrimSpace(line)) {
		case "BEGIN:VEVENT":
			current = map[string]icsProp{}
		case "END:VEVENT":
			if current != nil {
				event, ok, err := eventFromProps(current)
				if err != nil {
					return nil, err
				}
				if ok {
					events = append(events, event)
				}
			}
			current = nil
		default:
			if current == nil {
				continue
			}
			prop, ok := parseICSProp(line)
			if ok {
				current[prop.Name] = prop
			}
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].StartAt.Equal(events[j].StartAt) {
			return events[i].Summary < events[j].Summary
		}
		return events[i].StartAt.Before(events[j].StartAt)
	})
	return events, nil
}

type icsProp struct {
	Name   string
	Params map[string]string
	Value  string
}

func parseICSProp(line string) (icsProp, bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return icsProp{}, false
	}
	left, value := line[:idx], line[idx+1:]
	parts := strings.Split(left, ";")
	if len(parts) == 0 {
		return icsProp{}, false
	}
	prop := icsProp{Name: strings.ToUpper(parts[0]), Params: map[string]string{}, Value: unescapeICSText(value)}
	for _, part := range parts[1:] {
		key, val, ok := strings.Cut(part, "=")
		if ok {
			prop.Params[strings.ToUpper(key)] = strings.Trim(val, `"`)
		}
	}
	return prop, true
}

func eventFromProps(props map[string]icsProp) (calendarEvent, bool, error) {
	startProp, ok := props["DTSTART"]
	if !ok || strings.TrimSpace(startProp.Value) == "" {
		return calendarEvent{}, false, nil
	}
	start, allDay, err := parseICSTime(startProp)
	if err != nil {
		return calendarEvent{}, false, err
	}
	var endPtr *time.Time
	if endProp, ok := props["DTEND"]; ok && strings.TrimSpace(endProp.Value) != "" {
		end, _, err := parseICSTime(endProp)
		if err != nil {
			return calendarEvent{}, false, err
		}
		endPtr = &end
	}
	description, location, meetingDetails := splitMeetingDetails(
		propValue(props, "DESCRIPTION"),
		propValue(props, "LOCATION"),
		propValue(props, "URL"),
		propValue(props, "X-GOOGLE-CONFERENCE"),
	)
	event := calendarEvent{
		UID:            propValue(props, "UID"),
		Summary:        firstNonEmpty(propValue(props, "SUMMARY"), "Untitled Event"),
		Description:    description,
		MeetingDetails: meetingDetails,
		Location:       location,
		StartAt:        start,
		EndAt:          endPtr,
		AllDay:         allDay,
	}
	event.EventID = eventID(event)
	return event, true, nil
}

func parseICSTime(prop icsProp) (time.Time, bool, error) {
	value := strings.TrimSpace(prop.Value)
	if value == "" {
		return time.Time{}, false, fmt.Errorf("%s is empty", prop.Name)
	}
	if prop.Params["VALUE"] == "DATE" || len(value) == len("20060102") {
		t, err := time.ParseInLocation("20060102", value, time.Local)
		return t, true, err
	}
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse("20060102T150405Z", value)
		return t, false, err
	}
	loc := time.Local
	if tzid := strings.TrimSpace(prop.Params["TZID"]); tzid != "" {
		if loaded, err := time.LoadLocation(tzid); err == nil {
			loc = loaded
		}
	}
	t, err := time.ParseInLocation("20060102T150405", value, loc)
	return t, false, err
}

func unfoldICSLines(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		if len(out) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			out[len(out)-1] += strings.TrimLeft(line, " \t")
			continue
		}
		out = append(out, line)
	}
	return out
}

func unescapeICSText(value string) string {
	replacer := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return replacer.Replace(value)
}

func propValue(props map[string]icsProp, key string) string {
	if prop, ok := props[key]; ok {
		return prop.Value
	}
	return ""
}

var (
	meetingURLPattern       = regexp.MustCompile(`https?://(?:meet\.google\.com|hangouts\.google\.com)/[^\s<>"']+`)
	googleMeetMarkerPattern = regexp.MustCompile(`(?i)join\s+with\s+google\s+meet\s*:`)
	htmlBreakPattern        = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlParagraphGapPattern = regexp.MustCompile(`(?i)</p>\s*<p[^>]*>`)
	htmlTagPattern          = regexp.MustCompile(`<[^>]+>`)
)

func splitMeetingDetails(description, location, eventURL, conferenceHint string) (string, string, string) {
	var details []string
	if isMeetingDetail(location) {
		details = append(details, plainText(location))
		location = ""
	}
	if isMeetingDetail(eventURL) {
		details = append(details, eventURL)
	}
	description, extracted := extractMeetingDetailsFromDescription(description, conferenceHint)
	details = append(details, extracted...)
	return strings.TrimSpace(description), strings.TrimSpace(location), strings.Join(uniqueNonEmpty(details), "\n")
}

func extractMeetingDetailsFromDescription(description, conferenceHint string) (string, []string) {
	if strings.TrimSpace(description) == "" {
		return "", nil
	}
	normalized := htmlBreakPattern.ReplaceAllString(description, "\n")
	normalized = htmlParagraphGapPattern.ReplaceAllString(normalized, "</p>\n<p>")
	if strings.TrimSpace(conferenceHint) != "" {
		if loc := googleMeetMarkerPattern.FindStringIndex(normalized); loc != nil {
			publicDescription := strings.TrimSpace(normalized[:loc[0]])
			meetingDetails := strings.TrimSpace(normalized[loc[0]:])
			return publicDescription, []string{meetingDetails}
		}
	}
	lines := strings.Split(normalized, "\n")
	kept := make([]string, 0, len(lines))
	var details []string
	for _, line := range lines {
		plain := plainText(line)
		if isMeetingDetail(plain) || meetingURLPattern.MatchString(line) {
			details = append(details, firstNonEmpty(strings.Join(meetingURLPattern.FindAllString(line, -1), "\n"), plain))
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), uniqueNonEmpty(details)
}

func isMeetingDetail(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	return strings.Contains(value, "meet.google.com") ||
		strings.Contains(value, "hangouts.google.com") ||
		strings.Contains(value, "google meet") ||
		meetingURLPattern.MatchString(value)
}

func plainText(value string) string {
	value = htmlTagPattern.ReplaceAllString(value, " ")
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, line)
		}
	}
	return out
}

func eventID(event calendarEvent) string {
	seed := strings.Join([]string{event.UID, event.StartAt.UTC().Format(time.RFC3339Nano), event.Summary}, "|")
	sum := sha1.Sum([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func loadCalendarSettings(table string) (calendarSettings, error) {
	store := configStore{table: table}
	data, err := store.Load(configSection)
	if err != nil {
		return calendarSettings{}, err
	}
	settings := calendarSettings{
		Title:                    defaultTitle,
		RefreshIntervalMinutes:   defaultIntervalMin,
		DescriptionSummaryLength: defaultSummaryLength,
	}
	if v, ok := data["title"].(string); ok && strings.TrimSpace(v) != "" {
		settings.Title = strings.TrimSpace(v)
	}
	if v, ok := data["ics_url"].(string); ok {
		settings.ICSURL = strings.TrimSpace(v)
	}
	settings.RefreshIntervalMinutes = intSetting(data["refresh_interval_minutes"], defaultIntervalMin)
	settings.DescriptionSummaryLength = intSetting(data["description_summary_length"], defaultSummaryLength)
	settings.LoginRequiredForDetails = boolSetting(data["login_required_for_details"], false)
	if settings.RefreshIntervalMinutes <= 0 {
		settings.RefreshIntervalMinutes = defaultIntervalMin
	}
	if settings.DescriptionSummaryLength < 0 {
		settings.DescriptionSummaryLength = defaultSummaryLength
	}
	return settings, nil
}

func intSetting(raw any, fallback int) int {
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func boolSetting(raw any, fallback bool) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

type configStore struct {
	table string
}

func (s configStore) Load(section string) (map[string]any, error) {
	db, _, err := extension.OpenDB()
	if err != nil {
		return nil, err
	}
	return extension.DBConfigurationStore(db, s.table).Load(section)
}

func (s configStore) Save(section string, data map[string]any) error {
	db, _, err := extension.OpenDB()
	if err != nil {
		return err
	}
	return extension.DBConfigurationStore(db, s.table).Save(section, data)
}

func (app *calendarApp) render(name string, data map[string]any) extension.WireResponse {
	body, err := app.templates.WithFuncs(template.FuncMap{
		"formatEventDate": formatEventDate,
		"formatEventTime": formatEventTime,
		"hasTime":         hasTime,
	}).Execute(name, data)
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	return extension.HTML(http.StatusOK, body)
}

func displayEvents(events []calendarEvent, summaryLength int, u *url.URL, meetingDetailsAllowed bool) []calendarDisplayEvent {
	out := make([]calendarDisplayEvent, 0, len(events))
	for _, event := range events {
		display := calendarDisplayEvent{
			calendarEvent:   event,
			DescriptionHTML: renderDescriptionHTML(event.Description, summaryLength),
			DisplayLocation: event.Location,
			DetailURL:       setViewDateURL(u, "day", event.StartAt),
		}
		if strings.TrimSpace(event.MeetingDetails) != "" {
			if meetingDetailsAllowed {
				display.MeetingDetailsHTML = renderDescriptionHTML(event.MeetingDetails, 0)
			} else {
				display.MeetingDetailsLocked = true
			}
		}
		out = append(out, display)
	}
	return out
}

func renderDescriptionHTML(raw string, maxText int) template.HTML {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return template.HTML(sanitizeDescriptionHTML(raw, maxText))
}

var hrefPattern = regexp.MustCompile(`(?i)\bhref\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)

func sanitizeDescriptionHTML(raw string, maxText int) string {
	allowedTags := map[string]bool{
		"a": true, "b": true, "br": true, "div": true, "em": true, "i": true,
		"li": true, "ol": true, "p": true, "span": true, "strong": true, "u": true, "ul": true,
	}
	var out strings.Builder
	openTags := make([]string, 0, 8)
	remaining := maxText
	truncated := false
	skipUntil := ""

	writeText := func(segment string) {
		if segment == "" || truncated {
			return
		}
		text := htmlpkg.UnescapeString(segment)
		if maxText > 0 {
			runes := []rune(text)
			if len(runes) > remaining {
				text = string(runes[:remaining]) + "..."
				truncated = true
			}
			remaining -= len([]rune(text))
			if remaining < 0 {
				remaining = 0
			}
		}
		out.WriteString(htmlpkg.EscapeString(text))
	}

	for len(raw) > 0 && !truncated {
		tagStart := strings.Index(raw, "<")
		if tagStart < 0 {
			if skipUntil == "" {
				writeText(raw)
			}
			break
		}
		if skipUntil == "" {
			writeText(raw[:tagStart])
		}
		raw = raw[tagStart:]
		tagEnd := strings.Index(raw, ">")
		if tagEnd < 0 {
			writeText(raw)
			break
		}
		tag := raw[:tagEnd+1]
		raw = raw[tagEnd+1:]
		name, closing, selfClosing := parseHTMLTag(tag)
		if name == "" {
			continue
		}
		if skipUntil != "" {
			if closing && name == skipUntil {
				skipUntil = ""
			}
			continue
		}
		if name == "script" || name == "style" {
			if !closing {
				skipUntil = name
			}
			continue
		}
		if !allowedTags[name] {
			continue
		}
		if closing {
			out.WriteString("</" + name + ">")
			openTags = popOpenTag(openTags, name)
			continue
		}
		out.WriteString("<" + name)
		if name == "a" {
			if href := safeHref(tag); href != "" {
				out.WriteString(` href="` + htmlpkg.EscapeString(href) + `" rel="nofollow noopener noreferrer"`)
			}
		}
		if selfClosing || name == "br" {
			out.WriteString(">")
			continue
		}
		out.WriteString(">")
		openTags = append(openTags, name)
	}
	for i := len(openTags) - 1; i >= 0; i-- {
		out.WriteString("</" + openTags[i] + ">")
	}
	return out.String()
}

func parseHTMLTag(tag string) (name string, closing bool, selfClosing bool) {
	content := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tag, "<"), ">"))
	if content == "" || strings.HasPrefix(content, "!") || strings.HasPrefix(content, "?") {
		return "", false, false
	}
	if strings.HasPrefix(content, "/") {
		closing = true
		content = strings.TrimSpace(strings.TrimPrefix(content, "/"))
	}
	if strings.HasSuffix(content, "/") {
		selfClosing = true
		content = strings.TrimSpace(strings.TrimSuffix(content, "/"))
	}
	for i, r := range content {
		if !(r == '-' || r == ':' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			content = content[:i]
			break
		}
	}
	return strings.ToLower(content), closing, selfClosing
}

func safeHref(tag string) string {
	matches := hrefPattern.FindStringSubmatch(tag)
	if len(matches) == 0 {
		return ""
	}
	href := firstNonEmpty(matches[2], matches[3], matches[4])
	href = strings.TrimSpace(htmlpkg.UnescapeString(href))
	if href == "" {
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if parsed.Scheme == "" {
		if strings.HasPrefix(href, "/") || strings.HasPrefix(href, "#") {
			return href
		}
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto", "tel":
		return href
	default:
		return ""
	}
}

func popOpenTag(tags []string, name string) []string {
	for i := len(tags) - 1; i >= 0; i-- {
		if tags[i] == name {
			return tags[:i]
		}
	}
	return tags
}

func formatEventDate(event any) string {
	base := baseEvent(event)
	if base.AllDay {
		return base.StartAt.Format("Jan 2, 2006")
	}
	return base.StartAt.Format("Jan 2, 2006 3:04 PM")
}

func formatEventTime(event any) string {
	base := baseEvent(event)
	if base.AllDay {
		if base.EndAt != nil && base.EndAt.After(base.StartAt.AddDate(0, 0, 1)) {
			return base.StartAt.Format("Jan 2") + " - " + base.EndAt.AddDate(0, 0, -1).Format("Jan 2, 2006")
		}
		return "All day"
	}
	if base.EndAt != nil {
		return base.StartAt.Format("3:04 PM") + " - " + base.EndAt.Format("3:04 PM")
	}
	return base.StartAt.Format("3:04 PM")
}

func hasTime(event any) bool {
	return !baseEvent(event).AllDay
}

func baseEvent(event any) calendarEvent {
	switch e := event.(type) {
	case calendarEvent:
		return e
	case calendarDisplayEvent:
		return e.calendarEvent
	default:
		return calendarEvent{}
	}
}

func (app *calendarApp) eventsTable() string {
	return extension.TableName(app.info.TablePrefix(), "events")
}

func (app *calendarApp) syncTable() string {
	return extension.TableName(app.info.TablePrefix(), "sync")
}

func parseWireURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = "/"
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func normalizeView(view string) string {
	switch strings.ToLower(strings.TrimSpace(view)) {
	case "year", "month", "calendar", "day":
		return strings.ToLower(strings.TrimSpace(view))
	default:
		return "calendar"
	}
}

func viewDate(query url.Values, fallback time.Time) time.Time {
	for _, key := range []string{"date", "month"} {
		raw := strings.TrimSpace(query.Get(key))
		if raw == "" {
			continue
		}
		for _, layout := range []string{"2006-01-02", "2006-01"} {
			if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
				return t
			}
		}
	}
	if raw := strings.TrimSpace(query.Get("year")); raw != "" {
		if year, err := strconv.Atoi(raw); err == nil && year > 0 {
			return time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
		}
	}
	return fallback
}

func viewLinks(u *url.URL, activeView string, activeDate time.Time) []calendarViewLink {
	views := []calendarViewLink{
		{View: "calendar", Label: "Calendar"},
		{View: "month", Label: "Month List"},
		{View: "year", Label: "Year List"},
		{View: "day", Label: "Day"},
	}
	for i := range views {
		views[i].URL = setViewDateURL(u, views[i].View, activeDate)
		views[i].Active = views[i].View == activeView
	}
	return views
}

func periodURL(u *url.URL, view string, activeDate time.Time, delta int) string {
	switch view {
	case "year":
		return setViewDateURL(u, view, activeDate.AddDate(delta, 0, 0))
	case "day":
		return setViewDateURL(u, view, activeDate.AddDate(0, 0, delta))
	default:
		return setViewDateURL(u, view, activeDate.AddDate(0, delta, 0))
	}
}

func setViewDateURL(u *url.URL, view string, date time.Time) string {
	clone := *u
	q := clone.Query()
	q.Set("view", view)
	q.Del("year")
	q.Del("month")
	q.Del("date")
	switch view {
	case "year":
		q.Set("year", strconv.Itoa(date.Year()))
	case "day":
		q.Set("date", date.Format("2006-01-02"))
	default:
		q.Set("month", date.Format("2006-01"))
	}
	clone.RawQuery = q.Encode()
	if clone.Path == "" {
		clone.Path = "/"
	}
	return clone.String()
}

func viewLabel(view string) string {
	switch view {
	case "year":
		return "Year List"
	case "month":
		return "Month List"
	case "day":
		return "Day"
	default:
		return "Calendar"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
