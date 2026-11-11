package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/dock-scheduling/internal/adapters/inbound/http/page"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
)

// pathScac reads and validates {scac}: a path that is not 2..4 upper-case
// letters is 422 invalid-scac, the value-object rule of the registry (the
// carrier is addressed by its SCAC and by no other spelling).
func pathScac(r *http.Request) (string, error) {
	raw := chi.URLParam(r, "scac")
	if _, err := carrier.NewScac(raw); err != nil {
		return "", err
	}
	return raw, nil
}

// writeCarrier answers status with the carrier and its ETag (the version).
func writeCarrier(w http.ResponseWriter, status int, v views.CarrierView) {
	w.Header().Set("ETag", etag(v.Version))
	writeJSON(w, status, toCarrierBody(v))
}

// handlePutCarrier is PUT /carriers/{scac} (RegisterCarrier): 201 when the
// SCAC was new, 200 when it was already registered with exactly this data, 409
// carrier-already-registered when it is registered with other data.
func (s *Server) handlePutCarrier(w http.ResponseWriter, r *http.Request) {
	scac, err := pathScac(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req carrierRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.Service.RegisterCarrier(r.Context(), usecases.RegisterCarrierCommand{SCAC: scac, Name: req.Name, USDOT: deref(req.USDOT), MC: deref(req.MC)})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if res.Created {
		w.Header().Set("Location", "/carriers/"+res.Carrier.SCAC)
		writeCarrier(w, http.StatusCreated, res.Carrier)
		return
	}
	writeCarrier(w, http.StatusOK, res.Carrier)
}

// handleGetCarrier is GET /carriers/{scac} (GetCarrier).
func (s *Server) handleGetCarrier(w http.ResponseWriter, r *http.Request) {
	scac, err := pathScac(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.GetCarrier(r.Context(), scac)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeCarrier(w, http.StatusOK, v)
}

// handleListCarriers is GET /carriers (ListCarriers), keyset-paged by SCAC.
func (s *Server) handleListCarriers(w http.ResponseWriter, r *http.Request) {
	if name := page.Unknown(r, "status"); name != "" {
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
	status, err := carrierStatusQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.Service.ListCarriers(r.Context(), status, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := carrierPageBody{Items: make([]carrierBody, 0, len(res.Items))}
	for _, v := range res.Items {
		out.Items = append(out.Items, toCarrierBody(v))
	}
	out.NextCursor = orNil(res.NextCursor)
	writeJSON(w, http.StatusOK, out)
}

// carrierStatusQuery reads the optional status filter.
func carrierStatusQuery(r *http.Request) (carrier.Status, error) {
	raw, err := singleQuery(r, "status")
	if err != nil || raw == "" {
		return "", err
	}
	status, perr := carrier.ParseStatus(raw)
	if perr != nil {
		return "", invalidQuery("status", "must be ACTIVE or SUSPENDED")
	}
	return status, nil
}

// carrierTarget reads the path SCAC and the optional If-Match version of a
// state-changing carrier route; ok is false once the problem was written.
func carrierTarget(w http.ResponseWriter, r *http.Request) (scac string, version int64, ok bool) {
	scac, err := pathScac(r)
	if err != nil {
		writeError(w, r, err)
		return "", 0, false
	}
	version, err = ifMatchVersion(r)
	if err != nil {
		writeError(w, r, err)
		return "", 0, false
	}
	return scac, version, true
}

// handlePatchCarrier is PATCH /carriers/{scac} (UpdateCarrier): a JSON merge
// patch of name, usdot and mc.
func (s *Server) handlePatchCarrier(w http.ResponseWriter, r *http.Request) {
	scac, version, ok := carrierTarget(w, r)
	if !ok {
		return
	}
	var req carrierPatch
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.UpdateCarrier(r.Context(), usecases.UpdateCarrierCommand{
		SCAC: scac, Name: req.Name.pointer(), USDOT: req.USDOT.pointer(), MC: req.MC.pointer(), ExpectedVersion: version,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeCarrier(w, http.StatusOK, v)
}

// handleSuspendCarrier is POST /carriers/{scac}/suspensions (SuspendCarrier).
func (s *Server) handleSuspendCarrier(w http.ResponseWriter, r *http.Request) {
	scac, version, ok := carrierTarget(w, r)
	if !ok {
		return
	}
	var req suspensionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.SuspendCarrier(r.Context(), usecases.SuspendCarrierCommand{SCAC: scac, Reason: req.Reason, ExpectedVersion: version})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeCarrier(w, http.StatusOK, v)
}

// handleReinstateCarrier is POST /carriers/{scac}/reinstatements (ReinstateCarrier).
func (s *Server) handleReinstateCarrier(w http.ResponseWriter, r *http.Request) {
	scac, version, ok := carrierTarget(w, r)
	if !ok {
		return
	}
	v, err := s.Service.ReinstateCarrier(r.Context(), usecases.ReinstateCarrierCommand{SCAC: scac, ExpectedVersion: version})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeCarrier(w, http.StatusOK, v)
}
