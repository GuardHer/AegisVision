package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	// Embed IANA timezone database so schedule timezone validation works on Windows and minimal containers.
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/postgres"
	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

// SchedulesHandler manages reusable arming schedule templates (day/night shifts & holiday exceptions).
type SchedulesHandler struct {
	store *postgres.Store
	pub   EventPublisher
	log   *slog.Logger
}

// NewSchedulesHandler constructs a SchedulesHandler.
func NewSchedulesHandler(store *postgres.Store, pub EventPublisher, log *slog.Logger) *SchedulesHandler {
	return &SchedulesHandler{store: store, pub: pub, log: log}
}

type scheduleWeeklyWindowInput struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type scheduleIncludeDateInput struct {
	Date  string `json:"date"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

type scheduleMutationRequest struct {
	Name          string                      `json:"name" binding:"required"`
	Description   string                      `json:"description"`
	Timezone      string                      `json:"timezone"`
	WeeklyWindows []scheduleWeeklyWindowInput `json:"weekly_windows"`
	ExcludeDates  []string                    `json:"exclude_dates"`
	IncludeDates  []scheduleIncludeDateInput  `json:"include_dates"`
	Enabled       *bool                       `json:"enabled"`
}

func validateClockHHMM(raw string, allow24 bool) (int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("time %q must be HH:MM", raw)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("time %q is out of range", raw)
	}
	if allow24 && h == 24 && m == 0 {
		return 24 * 60, nil
	}
	if h < 0 || h > 23 {
		return 0, fmt.Errorf("time %q hour must be 00..23", raw)
	}
	return h*60 + m, nil
}

func validateScheduleRequest(req *scheduleMutationRequest) (string, json.RawMessage, json.RawMessage, json.RawMessage, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return "", nil, nil, nil, domain.NewInvalidArgumentError("schedule name is required", nil)
	}
	tz := strings.TrimSpace(req.Timezone)
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", nil, nil, nil, domain.NewInvalidArgumentError("invalid timezone", err)
	}

	weekly := req.WeeklyWindows
	if weekly == nil {
		weekly = []scheduleWeeklyWindowInput{}
	}
	for i, w := range weekly {
		if len(w.Days) == 0 {
			return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("weekly_windows[%d].days cannot be empty", i), nil)
		}
		for _, d := range w.Days {
			if d < 1 || d > 7 {
				return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("weekly_windows[%d].days must contain integers 1..7", i), nil)
			}
		}
		sMin, err := validateClockHHMM(w.Start, false)
		if err != nil {
			return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("weekly_windows[%d].start: %v", i, err), err)
		}
		eMin, err := validateClockHHMM(w.End, true)
		if err != nil {
			return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("weekly_windows[%d].end: %v", i, err), err)
		}
		if sMin == eMin {
			return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("weekly_windows[%d] start and end cannot be equal", i), nil)
		}
	}

	exclude := req.ExcludeDates
	if exclude == nil {
		exclude = []string{}
	}
	for i, d := range exclude {
		if _, err := time.Parse("2006-01-02", strings.TrimSpace(d)); err != nil {
			return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("exclude_dates[%d] must be YYYY-MM-DD", i), err)
		}
	}

	include := req.IncludeDates
	if include == nil {
		include = []scheduleIncludeDateInput{}
	}
	for i, inc := range include {
		if _, err := time.Parse("2006-01-02", strings.TrimSpace(inc.Date)); err != nil {
			return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("include_dates[%d].date must be YYYY-MM-DD", i), err)
		}
		sRaw := strings.TrimSpace(inc.Start)
		eRaw := strings.TrimSpace(inc.End)
		if sRaw != "" || eRaw != "" {
			if sRaw == "" || eRaw == "" {
				return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("include_dates[%d] must provide both start and end or omit both", i), nil)
			}
			sMin, err := validateClockHHMM(sRaw, false)
			if err != nil {
				return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("include_dates[%d].start: %v", i, err), err)
			}
			eMin, err := validateClockHHMM(eRaw, true)
			if err != nil {
				return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("include_dates[%d].end: %v", i, err), err)
			}
			if sMin >= eMin {
				return "", nil, nil, nil, domain.NewInvalidArgumentError(fmt.Sprintf("include_dates[%d] start must be earlier than end", i), nil)
			}
		}
	}

	wJSON, _ := json.Marshal(weekly)
	eJSON, _ := json.Marshal(exclude)
	iJSON, _ := json.Marshal(include)
	return tz, wJSON, eJSON, iJSON, nil
}

// List returns all arming schedules with linked rule counts.
func (h *SchedulesHandler) List(c *gin.Context) {
	items, err := h.store.Queries().ListArmingSchedules(c.Request.Context())
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, items)
}

// Create inserts a new arming schedule template and broadcasts a config change event.
func (h *SchedulesHandler) Create(c *gin.Context) {
	var req scheduleMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid schedule payload", err))
		return
	}
	tz, wJSON, eJSON, iJSON, valErr := validateScheduleRequest(&req)
	if valErr != nil {
		respondErr(c, valErr)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	created, err := h.store.Queries().CreateArmingSchedule(c.Request.Context(), sqlcgen.CreateArmingScheduleParams{
		ID:            uuid.Must(uuid.NewV7()),
		Name:          strings.TrimSpace(req.Name),
		Description:   strings.TrimSpace(req.Description),
		Timezone:      tz,
		WeeklyWindows: wJSON,
		ExcludeDates:  eJSON,
		IncludeDates:  iJSON,
		Enabled:       enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "schedule", "created", created.ID.String())
	respondCreated(c, created)
}

// Update modifies an arming schedule template and broadcasts a config change event for hot-reload.
func (h *SchedulesHandler) Update(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid schedule id", err))
		return
	}
	var req scheduleMutationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid schedule update payload", err))
		return
	}
	tz, wJSON, eJSON, iJSON, valErr := validateScheduleRequest(&req)
	if valErr != nil {
		respondErr(c, valErr)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	updated, err := h.store.Queries().UpdateArmingSchedule(c.Request.Context(), sqlcgen.UpdateArmingScheduleParams{
		ID:            id,
		Name:          strings.TrimSpace(req.Name),
		Description:   strings.TrimSpace(req.Description),
		Timezone:      tz,
		WeeklyWindows: wJSON,
		ExcludeDates:  eJSON,
		IncludeDates:  iJSON,
		Enabled:       enabled,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	emitConfigEvent(c.Request.Context(), h.pub, h.log, "schedule", "updated", updated.ID.String())
	respondOK(c, updated)
}

// Delete removes an unused arming schedule transactionally; returns 409 Conflict if any rule references it.
func (h *SchedulesHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondErr(c, domain.NewInvalidArgumentError("invalid schedule id", err))
		return
	}
	ctx := c.Request.Context()
	txErr := h.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		refCount, qErr := q.CountRulesUsingSchedule(ctx, uuid.NullUUID{UUID: id, Valid: true})
		if qErr != nil {
			return qErr
		}
		if refCount > 0 {
			return domain.NewConflictError("cannot delete schedule referenced by active rules", nil)
		}
		if delErr := q.DeleteArmingSchedule(ctx, id); delErr != nil {
			var pgErr *pgconn.PgError
			if errors.As(delErr, &pgErr) && pgErr.Code == "23503" {
				return domain.NewConflictError("cannot delete schedule referenced by rules", delErr)
			}
			return delErr
		}
		return nil
	})
	if txErr != nil {
		respondErr(c, txErr)
		return
	}
	emitConfigEvent(ctx, h.pub, h.log, "schedule", "deleted", id.String())
	respondOK(c, gin.H{"deleted": true})
}
