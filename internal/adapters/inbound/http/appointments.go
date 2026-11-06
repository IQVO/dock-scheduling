package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/inbound/http/page"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

// writeAppointment answers status with the appointment and its ETag.
func writeAppointment(w http.ResponseWriter, status int, v views.AppointmentView) {
	w.Header().Set("ETag", etag(v.Version))
	writeJSON(w, status, toAppointmentBody(v))
}

// handleBook is POST /appointments (BookAppointment).
func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	var req bookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.BookAppointment(r.Context(), usecases.BookCommand{
		DoorCode: req.DoorCode, Direction: req.Direction, Carrier: req.Carrier, CarrierSCAC: deref(req.CarrierSCAC),
		WindowStart: req.WindowStart.UTC(), WindowEnd: req.WindowEnd.UTC(), ASNNumbers: req.ASNNumbers, CPTID: deref(req.CPTID),
		ShipmentRefs: req.ShipmentRefs, ExpectedSealNumber: deref(req.ExpectedSealNumber),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/appointments/"+v.AppointmentID)
	writeAppointment(w, http.StatusCreated, v)
}

// handleGet is GET /appointments/{appointmentId} (GetAppointment).
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathAppointmentID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.GetAppointment(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeAppointment(w, http.StatusOK, v)
}

// listFilters are the declared filters of GET /appointments.
var listFilters = []string{"siteId", "doorCode", "state", "direction", "carrierScac", "atRisk", "from", "to"}

// handleList is GET /appointments (ListAppointments), keyset-paged.
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if name := page.Unknown(r, listFilters...); name != "" {
		writeError(w, r, errUnknownQuery(name))
		return
	}
	limit, err := page.Limit(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := page.After(r); err != nil {
		writeError(w, r, err)
		return
	}
	filter, err := listFilter(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.Service.ListAppointments(r.Context(), filter, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := pageBody{Items: make([]appointmentBody, 0, len(res.Items))}
	for _, v := range res.Items {
		out.Items = append(out.Items, toAppointmentBody(v))
	}
	out.NextCursor = orNil(res.NextCursor)
	writeJSON(w, http.StatusOK, out)
}

// listFilter reads and validates the filters of GET /appointments.
func listFilter(r *http.Request) (repository.AppointmentFilter, error) {
	var f repository.AppointmentFilter
	var err error
	for name, dst := range map[string]*string{"siteId": &f.SiteID, "doorCode": &f.DoorCode, "state": &f.State, "direction": &f.Direction, "carrierScac": &f.CarrierSCAC} {
		if *dst, err = singleQuery(r, name); err != nil {
			return f, err
		}
	}
	if f.State != "" && !validState(f.State) {
		return f, invalidQuery("state", "must be BOOKED, CHECKED_IN, COMPLETED, CANCELLED or NO_SHOW")
	}
	if f.Direction != "" {
		if _, err := shared.ParseDirection(f.Direction); err != nil {
			return f, invalidQuery("direction", "must be INBOUND or OUTBOUND")
		}
	}
	if f.AtRiskOnly, err = queryBool(r, "atRisk"); err != nil {
		return f, err
	}
	if f.From, err = queryTime(r, "from"); err != nil {
		return f, err
	}
	f.To, err = queryTime(r, "to")
	return f, err
}

func validState(s string) bool {
	switch dockappointment.State(s) {
	case dockappointment.Booked, dockappointment.CheckedIn, dockappointment.Completed, dockappointment.Cancelled, dockappointment.NoShow:
		return true
	}
	return false
}

// handleReschedule is POST /appointments/{appointmentId}/reschedules.
func (s *Server) handleReschedule(w http.ResponseWriter, r *http.Request) {
	id, version, ok := s.commandTarget(w, r)
	if !ok {
		return
	}
	var req rescheduleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.RescheduleAppointment(r.Context(), usecases.RescheduleCommand{
		AppointmentID: id, DoorCode: deref(req.DoorCode), WindowStart: req.WindowStart.UTC(), WindowEnd: req.WindowEnd.UTC(), ExpectedVersion: version,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeAppointment(w, http.StatusOK, v)
}

// handleCancel is POST /appointments/{appointmentId}/cancellations. Over REST
// only CARRIER_REQUEST and SITE_REQUEST are accepted (the others are set by
// the policies).
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id, version, ok := s.commandTarget(w, r)
	if !ok {
		return
	}
	var req cancelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Reason != string(dockappointment.CarrierRequest) && req.Reason != string(dockappointment.SiteRequest) {
		writeError(w, r, dockappointment.ErrInvalidReason)
		return
	}
	v, err := s.Service.CancelAppointment(r.Context(), usecases.CancelCommand{AppointmentID: id, Reason: req.Reason, ExpectedVersion: version})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeAppointment(w, http.StatusOK, v)
}

// handleEta is POST /appointments/{appointmentId}/eta-reports (ReportEta).
func (s *Server) handleEta(w http.ResponseWriter, r *http.Request) {
	id, err := pathAppointmentID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req etaRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.Service.ReportEta(r.Context(), usecases.EtaCommand{AppointmentID: id, EtaAt: req.EtaAt.UTC(), Source: req.Source})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(res.Appointment.Version))
	writeJSON(w, http.StatusCreated, etaResultBody{Appointment: toAppointmentBody(res.Appointment), AtRisk: res.AtRisk})
}

// commandTarget reads the path id and the optional If-Match version of a
// state-changing sub-resource; ok is false once the problem was written.
func (s *Server) commandTarget(w http.ResponseWriter, r *http.Request) (id string, version int64, ok bool) {
	id, err := pathAppointmentID(r)
	if err != nil {
		writeError(w, r, err)
		return "", 0, false
	}
	version, err = ifMatchVersion(r)
	if err != nil {
		writeError(w, r, err)
		return "", 0, false
	}
	return id, version, true
}

// handleAvailability is GET /doors/{doorCode}/availability.
func (s *Server) handleAvailability(w http.ResponseWriter, r *http.Request) {
	code, err := pathDoorCode(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if name := unknownParam(r, "from", "to", "minutes"); name != "" {
		writeError(w, r, errUnknownQuery(name))
		return
	}
	from, to, minutes, err := availabilityQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.GetDoorAvailability(r.Context(), code, from, to, minutes)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAvailabilityBody(v))
}

func availabilityQuery(r *http.Request) (from, to time.Time, minutes int, err error) {
	if from, err = queryTime(r, "from"); err != nil {
		return from, to, 0, err
	}
	if to, err = queryTime(r, "to"); err != nil {
		return from, to, 0, err
	}
	if from.IsZero() || to.IsZero() {
		return from, to, 0, usecases.ErrInvalidAvailabilityQuery
	}
	raw, err := singleQuery(r, "minutes")
	if err != nil || raw == "" {
		return from, to, 0, err
	}
	minutes, perr := strconv.Atoi(raw)
	if perr != nil || minutes < 1 || minutes > 240 {
		return from, to, 0, invalidQuery("minutes", "must be an integer between 1 and 240")
	}
	return from, to, minutes, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
