package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/application/views"
)

// handleGetCalendar is GET /sites/{siteId}/dock-calendar (GetDockCalendar).
func (s *Server) handleGetCalendar(w http.ResponseWriter, r *http.Request) {
	site, err := pathSiteID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.GetDockCalendar(r.Context(), site)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCalendarBody(v))
}

// handlePutHours is PUT /sites/{siteId}/dock-calendar/operating-hours.
func (s *Server) handlePutHours(w http.ResponseWriter, r *http.Request) {
	site, err := pathSiteID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req operatingHoursRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	rows := make([]usecases.HoursInput, 0, len(req.OperatingHours))
	for _, h := range req.OperatingHours {
		rows = append(rows, usecases.HoursInput{DoorCode: deref(h.DoorCode), Weekday: h.Weekday, OpensAt: h.OpensAt, ClosesAt: h.ClosesAt, Timezone: h.Timezone})
	}
	v, err := s.Service.DeclareOperatingHours(r.Context(), site, rows)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCalendarBody(v))
}

// handlePutSettings is PUT /sites/{siteId}/dock-calendar/settings.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	site, err := pathSiteID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req settingsBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.DeclareCalendarSettings(r.Context(), site, views.SettingsView{
		MaxWindowMinutes: req.MaxWindowMinutes, SlotGranularityMinutes: req.SlotGranularityMinutes,
		ArrivalToleranceMinutes: req.ArrivalToleranceMinutes, NoShowGraceMinutes: req.NoShowGraceMinutes,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCalendarBody(v))
}

// handleScheduleClosure is POST /sites/{siteId}/dock-calendar/closures.
func (s *Server) handleScheduleClosure(w http.ResponseWriter, r *http.Request) {
	site, err := pathSiteID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req closureRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.ScheduleDoorClosure(r.Context(), usecases.ClosureCommand{
		SiteID: site, DoorCode: req.DoorCode, From: req.From.UTC(), To: req.To.UTC(), Reason: req.Reason,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toClosureBody(v))
}

// handleLiftClosure is POST /sites/{siteId}/dock-calendar/closures/{closureId}/lifts.
func (s *Server) handleLiftClosure(w http.ResponseWriter, r *http.Request) {
	site, err := pathSiteID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.LiftDoorClosure(r.Context(), site, chi.URLParam(r, "closureId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toClosureBody(v))
}

// handleNoShowSweep is POST /no-show-sweeps (the cron-driven NoShowSweep).
func (s *Server) handleNoShowSweep(w http.ResponseWriter, r *http.Request) {
	res, err := s.Service.RunNoShowSweep(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	ids := res.AppointmentIDs
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, sweepBody{Count: res.Count, AppointmentIDs: ids, RanAt: res.RanAt.UTC()})
}
