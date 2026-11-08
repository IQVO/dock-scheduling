package dockappointment

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

const maxEtaSourceLength = 64

// Reschedule moves a BOOKED appointment to another window and optionally
// another door (an empty doorCode keeps the door) and raises
// DockAppointmentRescheduled. The calendar and overlap checks of the new
// door/window are the caller's, through the domain services. Clears an
// at-risk flag and the ETA of the old window.
func (a *DockAppointment) Reschedule(doorCode string, start, end time.Time, r Rules, now time.Time) ([]Event, error) {
	if a.state != Booked {
		return nil, ErrIllegalTransition
	}
	door := a.door
	if doorCode != "" {
		var err error
		if door, err = shared.NewDoorCode(doorCode); err != nil {
			return nil, err
		}
	}
	w, err := NewWindow(start, end, r)
	if err != nil {
		return nil, err
	}
	if w.start.Before(now) {
		return nil, ErrWindowInPast
	}
	previous, previousDoor := a.window, a.door
	a.window, a.door, a.override = w, door, false
	a.eta, a.atRisk = nil, ""
	a.touch(now)
	return []Event{RescheduledEvent{
		Header: a.header(now), DoorCode: string(a.door), PreviousDoorCode: string(previousDoor), WindowStart: w.start, WindowEnd: w.end,
		PreviousWindowStart: previous.start, PreviousWindowEnd: previous.end, ToleranceMinutes: r.ToleranceMinutes(),
	}}, nil
}

// Cancel cancels a BOOKED or CHECKED_IN appointment and raises
// DockAppointmentCancelled.
func (a *DockAppointment) Cancel(reason Reason, now time.Time) ([]Event, error) {
	if _, err := ParseReason(string(reason)); err != nil {
		return nil, err
	}
	if !a.state.Active() {
		return nil, ErrIllegalTransition
	}
	a.state, a.reason, a.atRisk = Cancelled, reason, ""
	a.touch(now)
	return []Event{CancelledEvent{Header: a.header(now), DoorCode: string(a.door), Reason: reason}}, nil
}

// ReportEta stores the carrier's latest ETA on a BOOKED appointment. An ETA
// later than window start plus the arrival tolerance flags the appointment at
// risk (LATE_ETA) and raises DockAppointmentAtRisk once for this report; an
// on-time ETA clears a LATE_ETA flag. Nothing reschedules automatically.
func (a *DockAppointment) ReportEta(etaAt time.Time, source string, r Rules, now time.Time) ([]Event, error) {
	if a.state != Booked {
		return nil, ErrIllegalTransition
	}
	if etaAt.IsZero() || utf8.RuneCountInString(source) > maxEtaSourceLength || strings.TrimSpace(source) == "" {
		return nil, ErrInvalidEta
	}
	a.eta = &Eta{At: etaAt.UTC(), ReportedAt: now.UTC(), Source: source}
	a.touch(now)
	if !etaAt.After(a.window.start.Add(r.ArrivalTolerance)) {
		if a.atRisk == LateEta {
			a.atRisk = ""
		}
		return nil, nil
	}
	a.atRisk = LateEta
	return []Event{a.atRiskEvent(LateEta, etaAt, now)}, nil
}

// MarkAtRisk flags a BOOKED appointment whose door window is threatened by a
// door reassignment (DOOR_REASSIGNED_OVERLAP) and raises DockAppointmentAtRisk.
func (a *DockAppointment) MarkAtRisk(cause AtRiskCause, now time.Time) ([]Event, error) {
	if a.state != Booked {
		return nil, ErrIllegalTransition
	}
	a.atRisk = cause
	a.touch(now)
	return []Event{a.atRiskEvent(cause, time.Time{}, now)}, nil
}

func (a *DockAppointment) atRiskEvent(cause AtRiskCause, eta, now time.Time) AtRisk {
	return AtRisk{Header: a.header(now), DoorCode: string(a.door), EtaAt: eta.UTC(), WindowStart: a.window.start, WindowEnd: a.window.end, Cause: cause}
}

// RecordArrival applies the gate arrival (a yard fact) to a BOOKED
// appointment: CHECKED_IN, the status computed against the window and the
// site's tolerance (EARLY before start - tolerance, LATE after start +
// tolerance), and raises DockAppointmentCheckedIn.
func (a *DockAppointment) RecordArrival(at time.Time, visitID string, r Rules, now time.Time) ([]Event, error) {
	if a.state != Booked {
		return nil, ErrIllegalTransition
	}
	status := Classify(a.window, r.ArrivalTolerance, at)
	a.state, a.atRisk = CheckedIn, ""
	a.arrival = &Arrival{At: at.UTC(), Status: status, VisitID: visitID}
	a.touch(now)
	return []Event{CheckedInEvent{Header: a.header(now), DoorCode: string(a.door), CheckedInAt: at.UTC(), ArrivalStatus: status, VisitID: visitID}}, nil
}

// ReassignDoor moves a BOOKED or CHECKED_IN appointment to the door the
// trailer physically docked at (the physical fact wins: no overlap refusal)
// and raises DockAppointmentDoorReassigned. The same door is a no-op.
func (a *DockAppointment) ReassignDoor(doorCode string, now time.Time) ([]Event, error) {
	if !a.state.Active() {
		return nil, ErrIllegalTransition
	}
	door, err := shared.NewDoorCode(doorCode)
	if err != nil {
		return nil, err
	}
	if door == a.door {
		return nil, nil
	}
	from := a.door
	a.door, a.override = door, true
	a.touch(now)
	return []Event{DoorReassigned{Header: a.header(now), FromDoorCode: string(from), ToDoorCode: string(door)}}, nil
}

// RecordDoorRelease applies the release of the door (a yard fact): a
// CHECKED_IN appointment completes; a BOOKED one completes with the arrival
// recorded as implicit (the release outran the gate fact). Raises
// DockAppointmentCompleted.
func (a *DockAppointment) RecordDoorRelease(at time.Time, now time.Time) ([]Event, error) {
	switch a.state {
	case CheckedIn:
	case Booked:
		a.arrival = &Arrival{At: at.UTC(), Status: OnTime, Implicit: true}
	default:
		return nil, ErrIllegalTransition
	}
	a.state, a.atRisk, a.completed = Completed, "", at.UTC()
	a.touch(now)
	return []Event{CompletedEvent{Header: a.header(now), DoorCode: string(a.door), CompletedAt: at.UTC()}}, nil
}

// MarkNoShow marks a BOOKED appointment whose window end plus the site's
// no-show grace has passed as NO_SHOW (terminal) and raises
// DockAppointmentNoShow.
func (a *DockAppointment) MarkNoShow(r Rules, now time.Time) ([]Event, error) {
	if a.state != Booked {
		return nil, ErrIllegalTransition
	}
	if now.Before(a.window.end.Add(r.NoShowGrace)) {
		return nil, ErrNoShowTooEarly
	}
	a.state, a.atRisk = NoShow, ""
	a.touch(now)
	return []Event{NoShowEvent{Header: a.header(now), DoorCode: string(a.door), Direction: a.direction, CarrierSCAC: a.carrier.SCAC(), WindowEnd: a.window.end}}, nil
}

// NoShowDueAt returns the instant from which a BOOKED appointment may be
// marked no-show under the given rules.
func (a *DockAppointment) NoShowDueAt(r Rules) time.Time { return a.window.end.Add(r.NoShowGrace) }
