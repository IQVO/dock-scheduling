package http

import (
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// maxReportRange is the longest range the adherence report answers.
const maxReportRange = 92 * 24 * time.Hour

type adherenceRowBody struct {
	SiteID      string  `json:"siteId"`
	CarrierSCAC string  `json:"carrierScac"`
	OnTime      int     `json:"onTime"`
	Early       int     `json:"early"`
	Late        int     `json:"late"`
	NoShow      int     `json:"noShow"`
	Total       int     `json:"total"`
	OnTimeShare float64 `json:"onTimeShare"`
	EarlyShare  float64 `json:"earlyShare"`
	LateShare   float64 `json:"lateShare"`
	NoShowShare float64 `json:"noShowShare"`
}

type adherenceReportBody struct {
	From  time.Time          `json:"from"`
	To    time.Time          `json:"to"`
	Items []adherenceRowBody `json:"items"`
}

// handleScheduleAdherence is GET /reports/schedule-adherence (D-13). It serves
// the contract shape from the Adherence read-side port (the analytics store,
// ADR 0006); the route lives on NewReportsRouter, not on the OLTP router.
func (s *Server) handleScheduleAdherence(w http.ResponseWriter, r *http.Request) {
	if name := unknownParam(r, "siteId", "carrierScac", "from", "to"); name != "" {
		writeError(w, r, errUnknownQuery(name))
		return
	}
	q, err := adherenceQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var rows []repository.AdherenceRow
	if s.Adherence != nil {
		if rows, err = s.Adherence.ScheduleAdherence(r.Context(), q); err != nil {
			writeError(w, r, err)
			return
		}
	}
	out := adherenceReportBody{From: q.From, To: q.To, Items: make([]adherenceRowBody, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, toAdherenceRow(row))
	}
	writeJSON(w, http.StatusOK, out)
}

func adherenceQuery(r *http.Request) (repository.AdherenceQuery, error) {
	var q repository.AdherenceQuery
	var err error
	if q.SiteID, err = singleQuery(r, "siteId"); err != nil {
		return q, err
	}
	if q.CarrierSCAC, err = singleQuery(r, "carrierScac"); err != nil {
		return q, err
	}
	if q.From, err = queryTime(r, "from"); err != nil {
		return q, err
	}
	if q.To, err = queryTime(r, "to"); err != nil {
		return q, err
	}
	if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) || q.To.Sub(q.From) > maxReportRange {
		return q, fmt.Errorf("%w: from and to are required, from < to, at most 92 days", errInvalidReportRange)
	}
	return q, nil
}

func toAdherenceRow(row repository.AdherenceRow) adherenceRowBody {
	total := row.OnTime + row.Early + row.Late + row.NoShow
	share := func(n int) float64 {
		if total == 0 {
			return 0
		}
		return math.Round(float64(n)/float64(total)*10000) / 10000
	}
	return adherenceRowBody{
		SiteID: row.SiteID, CarrierSCAC: row.CarrierSCAC, OnTime: row.OnTime, Early: row.Early, Late: row.Late, NoShow: row.NoShow, Total: total,
		OnTimeShare: share(row.OnTime), EarlyShare: share(row.Early), LateShare: share(row.Late), NoShowShare: share(row.NoShow),
	}
}
