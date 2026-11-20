package cleanedweb

import (
	"context"
	"encoding/json"
	"net/http"
)

// SchedulesService creates and manages recurring runs of a catalog endpoint or one of your sources.
// Each due slot starts an ordinary charged run, owned by your workspace and readable with
// Runs. The creating key stands behind the schedule: it must keep the run scope.
type SchedulesService struct{ c *Client }

// Cadence periods.
const (
	EveryHour = "hour"
	EveryDay  = "day"
	EveryWeek = "week"
)

// ScheduleParams create a schedule. Give APIID and EndpointID (a catalog API) or SourceID (one of
// your own), not both.
type ScheduleParams struct {
	APIID      string
	EndpointID string
	SourceID   string
	// Version is the major of a catalog API to schedule; nil keeps the listing's current one.
	Version *int
	// Input is the API's input; every required input is given now and used for each slot.
	Input          map[string]any
	Fields         []string
	MaxItems       *int
	TimeoutSeconds *int
	// Every is EveryHour, EveryDay or EveryWeek (UTC, not more often than hourly).
	Every string
	// AtUTC is the UTC time of day, "HH:MM" (for hourly, the minute past each hour; default "00:00").
	AtUTC string
	// Weekday is for EveryWeek: 0 Monday to 6 Sunday (default Monday).
	Weekday *int
	// RunNow also runs once at the next tick, not only from the next slot.
	RunNow bool
	Name   string
	// IdempotencyKey is the retry key: the same key and arguments answer the same schedule and never
	// create a second. Generated and returned in Schedule.IdempotencyKey when empty.
	IdempotencyKey string
}

// Cadence says when a schedule is due.
type Cadence struct {
	Every   string `json:"every"`
	AtUTC   string `json:"at_utc"`
	Weekday *int   `json:"weekday"`
}

// Schedule is a schedule as the API answers it.
type Schedule struct {
	ScheduleID  string `json:"schedule_id"`
	Name        string `json:"name"`
	State       string `json:"state"` // "active" or "paused"
	PauseReason string `json:"pause_reason"`
	// Target is {"api_id","endpoint_id"} or {"source_id"}.
	Target              Object   `json:"target"`
	SourceID            string   `json:"source_id"`
	PinnedVersion       string   `json:"pinned_version"`
	Input               Object   `json:"input"`
	Fields              []string `json:"fields"`
	MaxItems            *int     `json:"max_items"`
	TimeoutSeconds      int      `json:"timeout_seconds"`
	Cadence             Cadence  `json:"cadence"`
	NextDueAt           string   `json:"next_due_at"`
	LastSlotAt          string   `json:"last_slot_at"`
	LastRunID           string   `json:"last_run_id"`
	LastStatus          string   `json:"last_status"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	SkippedSlots        int      `json:"skipped_slots"`
	// Attention lists "deferred", "overdue" or "previous_run_stuck".
	Attention []string `json:"attention"`
	CreatedAt string   `json:"created_at"`
	// InflightRunID is, on a pause answer, the run a ticker was already starting.
	InflightRunID string `json:"inflight_run_id"`
	// Replayed is true when the key already made this schedule (Idempotent-Replayed header).
	Replayed bool `json:"-"`
	// IdempotencyKey is the key the create call sent.
	IdempotencyKey string `json:"-"`
	Raw            Object `json:"-"`
}

// UnmarshalJSON keeps nullable fields as zero values and the whole body in Raw.
func (s *Schedule) UnmarshalJSON(data []byte) error {
	type plain Schedule
	aux := struct {
		*plain
		PauseReason   *string `json:"pause_reason"`
		Name          *string `json:"name"`
		PinnedVersion *string `json:"pinned_version"`
		SourceID      *string `json:"source_id"`
		NextDueAt     *string `json:"next_due_at"`
		LastSlotAt    *string `json:"last_slot_at"`
		LastRunID     *string `json:"last_run_id"`
		LastStatus    *string `json:"last_status"`
		InflightRunID *string `json:"inflight_run_id"`
	}{plain: (*plain)(s)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&s.PauseReason, aux.PauseReason)
	set(&s.Name, aux.Name)
	set(&s.PinnedVersion, aux.PinnedVersion)
	set(&s.SourceID, aux.SourceID)
	set(&s.NextDueAt, aux.NextDueAt)
	set(&s.LastSlotAt, aux.LastSlotAt)
	set(&s.LastRunID, aux.LastRunID)
	set(&s.LastStatus, aux.LastStatus)
	set(&s.InflightRunID, aux.InflightRunID)
	_ = decodeNumbers(data, &s.Raw)
	if replayed, ok := s.Raw["replayed"].(bool); ok && replayed {
		s.Replayed = true
	}
	return nil
}

// ScheduleList is the answer of Schedules.List.
type ScheduleList struct {
	Schedules []Schedule `json:"schedules"`
	// Limit is the most schedules a workspace holds.
	Limit int `json:"limit"`
}

// ScheduleDeleted is the answer of Schedules.Delete.
type ScheduleDeleted struct {
	ScheduleID string `json:"schedule_id"`
	Deleted    bool   `json:"deleted"`
	// InflightRunID is the run a ticker was already starting, if any.
	InflightRunID string `json:"-"`
}

func (p *ScheduleParams) body() (map[string]any, error) {
	if p == nil {
		return nil, invalidArgument("schedule parameters are required")
	}
	if p.MaxItems != nil && *p.MaxItems < 1 {
		return nil, invalidArgument("MaxItems must be an integer >= 1")
	}
	hasCatalog := p.APIID != "" || p.EndpointID != ""
	if (p.SourceID != "") == hasCatalog || (hasCatalog && (p.APIID == "" || p.EndpointID == "")) {
		return nil, invalidArgument("give APIID and EndpointID, or SourceID, not both")
	}
	if p.Every == "" {
		return nil, invalidArgument("Every must be hour, day or week")
	}
	body := map[string]any{"every": p.Every}
	set := func(k string, v string) {
		if v != "" {
			body[k] = v
		}
	}
	set("api_id", p.APIID)
	set("endpoint_id", p.EndpointID)
	set("source_id", p.SourceID)
	set("at_utc", p.AtUTC)
	set("name", p.Name)
	if p.Input != nil {
		body["input"] = p.Input
	}
	if p.Fields != nil {
		body["fields"] = p.Fields
	}
	if p.Version != nil {
		body["version"] = *p.Version
	}
	if p.MaxItems != nil {
		body["max_items"] = *p.MaxItems
	}
	if p.TimeoutSeconds != nil {
		body["timeout_seconds"] = *p.TimeoutSeconds
	}
	if p.Weekday != nil {
		body["weekday"] = *p.Weekday
	}
	if p.RunNow {
		body["run_now"] = true
	}
	return body, nil
}

const schedulesPath = "/v1/schedules"

// Create creates a schedule (run scope). The retry key goes in the Idempotency-Key header: a
// retry with the same key and arguments answers the same schedule (Schedule.Replayed) and never
// makes a second one. Another request under the same key is an idempotency conflict. A deleted
// schedule is never revived by a retry.
func (s *SchedulesService) Create(ctx context.Context, p *ScheduleParams) (*Schedule, error) {
	body, err := p.body()
	if err != nil {
		return nil, err
	}
	key, err := keyOrNew(p.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	resp, err := s.c.do(ctx, &request{method: http.MethodPost, url: s.c.url(schedulesPath), body: body, auth: true, key: key})
	if err != nil {
		return nil, err
	}
	sch, err := decodeSchedule(resp.body)
	if err != nil {
		return nil, err
	}
	sch.IdempotencyKey = key
	if resp.header.Get("Idempotent-Replayed") == "true" {
		sch.Replayed = true
	}
	return sch, nil
}

func decodeSchedule(body []byte) (*Schedule, error) {
	var sch Schedule
	if err := decodeJSON(body, &sch); err != nil {
		return nil, err
	}
	return &sch, nil
}

// List reads the schedules of your workspace, paused ones included (read scope).
func (s *SchedulesService) List(ctx context.Context) (*ScheduleList, error) {
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, url: s.c.url(schedulesPath), auth: true})
	if err != nil {
		return nil, err
	}
	var out ScheduleList
	if err := decodeJSON(resp.body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get reads one schedule (read scope).
func (s *SchedulesService) Get(ctx context.Context, scheduleID string) (*Schedule, error) {
	return s.read(ctx, http.MethodGet, schedulesPath+"/"+seg(scheduleID))
}

// Pause pauses a schedule (run scope): no slot starts after this answer, except one a ticker was
// already starting (Schedule.InflightRunID).
func (s *SchedulesService) Pause(ctx context.Context, scheduleID string) (*Schedule, error) {
	return s.read(ctx, http.MethodPost, schedulesPath+"/"+seg(scheduleID)+"/pause")
}

// Resume resumes a paused schedule from now (run scope). Slots missed while paused are counted in
// SkippedSlots. Only a paused schedule can be resumed.
func (s *SchedulesService) Resume(ctx context.Context, scheduleID string) (*Schedule, error) {
	return s.read(ctx, http.MethodPost, schedulesPath+"/"+seg(scheduleID)+"/resume")
}

// Delete deletes a schedule for good (run scope).
func (s *SchedulesService) Delete(ctx context.Context, scheduleID string) (*ScheduleDeleted, error) {
	resp, err := s.c.do(ctx, &request{method: http.MethodDelete, url: s.c.url(schedulesPath + "/" + seg(scheduleID)), auth: true})
	if err != nil {
		return nil, err
	}
	var raw struct {
		ScheduleDeleted
		Inflight *string `json:"inflight_run_id"`
	}
	if err := decodeJSON(resp.body, &raw); err != nil {
		return nil, err
	}
	out := raw.ScheduleDeleted
	if raw.Inflight != nil {
		out.InflightRunID = *raw.Inflight
	}
	return &out, nil
}

func (s *SchedulesService) read(ctx context.Context, method, path string) (*Schedule, error) {
	resp, err := s.c.do(ctx, &request{method: method, url: s.c.url(path), auth: true})
	if err != nil {
		return nil, err
	}
	return decodeSchedule(resp.body)
}
