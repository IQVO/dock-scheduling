package usecases

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/views"
	"github.com/claudioed/dock-scheduling/internal/domain/carrier"
)

// carrierPreconditionError is the refusal of an If-Match version that is not
// the current version of the carrier. It IS ErrPreconditionFailed (errors.Is),
// so both adapters answer it like the appointment's: 412 precondition-failed.
type carrierPreconditionError struct {
	scac    carrier.Scac
	version int64
}

func (e *carrierPreconditionError) Error() string {
	return fmt.Sprintf("the carrier version differs from If-Match: %s is now at version %d", e.scac, e.version)
}

// Is reports a match with ErrPreconditionFailed.
func (e *carrierPreconditionError) Is(target error) bool { return target == ErrPreconditionFailed }

// RegisterCarrierCommand is RegisterCarrier.
type RegisterCarrierCommand struct {
	SCAC  string
	Name  string
	USDOT string
	MC    string
}

// RegisterCarrier puts a carrier in the registry (RegisterCarrier). It is
// idempotent by SCAC: an unknown SCAC is created (ACTIVE, version 1,
// CarrierRegistered); a registered SCAC with exactly the same data answers the
// carrier as it is and changes nothing; the same SCAC with other data is
// refused with carrier.ErrCarrierAlreadyRegistered, a change goes through
// UpdateCarrier.
func (s *Service) RegisterCarrier(ctx context.Context, cmd RegisterCarrierCommand) (views.CarrierResult, error) {
	var out views.CarrierResult
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		scac, err := carrier.NewScac(cmd.SCAC)
		if err != nil {
			return err
		}
		id, err := carrier.NewIdentity(cmd.Name, cmd.USDOT, cmd.MC)
		if err != nil {
			return err
		}
		c, err := s.d.Carriers.GetForUpdate(ctx, scac)
		if errors.Is(err, ports.ErrNotFound) {
			created, events, err := carrier.Register(cmd.SCAC, cmd.Name, cmd.USDOT, cmd.MC, s.d.Clock.Now())
			if err != nil {
				return err
			}
			out = views.CarrierResult{Carrier: toCarrierView(created), Created: true}
			return s.saveCarrier(ctx, created, 0, events)
		}
		if err != nil {
			return err
		}
		if err := c.EnsureRegistered(id); err != nil {
			return err
		}
		out = views.CarrierResult{Carrier: toCarrierView(c)}
		return nil
	})
	return out, err
}

// UpdateCarrierCommand is UpdateCarrier: a nil field keeps the value, a
// pointer to "" clears the optional USDOT or MC. ExpectedVersion 0 means no
// precondition.
type UpdateCarrierCommand struct {
	SCAC            string
	Name            *string
	USDOT           *string
	MC              *string
	ExpectedVersion int64
}

// UpdateCarrier changes the name, USDOT and/or MC of a registered carrier
// (UpdateCarrier). A change that leaves the data as it is answers the carrier
// and raises nothing. Otherwise CarrierRegistered carries the new snapshot.
func (s *Service) UpdateCarrier(ctx context.Context, cmd UpdateCarrierCommand) (views.CarrierView, error) {
	return s.changeCarrier(ctx, cmd.SCAC, cmd.ExpectedVersion, func(c *carrier.Carrier) ([]carrier.Event, error) {
		return c.Update(carrier.Change{Name: cmd.Name, USDOT: cmd.USDOT, MC: cmd.MC}, s.d.Clock.Now())
	})
}

// SuspendCarrierCommand is SuspendCarrier.
type SuspendCarrierCommand struct {
	SCAC            string
	Reason          string
	ExpectedVersion int64
}

// SuspendCarrier stops a carrier booking (SuspendCarrier): a booking or
// reschedule that names its SCAC is refused until it is reinstated. Existing
// appointments are left alone. Raises CarrierSuspended.
func (s *Service) SuspendCarrier(ctx context.Context, cmd SuspendCarrierCommand) (views.CarrierView, error) {
	return s.changeCarrier(ctx, cmd.SCAC, cmd.ExpectedVersion, func(c *carrier.Carrier) ([]carrier.Event, error) {
		return c.Suspend(cmd.Reason, s.d.Clock.Now())
	})
}

// ReinstateCarrierCommand is ReinstateCarrier.
type ReinstateCarrierCommand struct {
	SCAC            string
	ExpectedVersion int64
}

// ReinstateCarrier lets a suspended carrier book again (ReinstateCarrier).
// Raises CarrierReinstated.
func (s *Service) ReinstateCarrier(ctx context.Context, cmd ReinstateCarrierCommand) (views.CarrierView, error) {
	return s.changeCarrier(ctx, cmd.SCAC, cmd.ExpectedVersion, func(c *carrier.Carrier) ([]carrier.Event, error) {
		return c.Reinstate(s.d.Clock.Now())
	})
}

// changeCarrier is the shape of every command on a registered carrier: lock
// and load it, check the caller's expected version, run the aggregate's
// command, save guarded by the loaded version and enqueue what it raised.
func (s *Service) changeCarrier(ctx context.Context, rawSCAC string, expected int64, run func(*carrier.Carrier) ([]carrier.Event, error)) (views.CarrierView, error) {
	var out views.CarrierView
	err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
		scac, err := carrier.NewScac(rawSCAC)
		if err != nil {
			return err
		}
		c, err := s.loadCarrier(ctx, scac, true)
		if err != nil {
			return err
		}
		if expected != 0 && c.Version() != expected {
			return &carrierPreconditionError{scac: scac, version: c.Version()}
		}
		loaded := c.Version()
		events, err := run(c)
		if err != nil {
			return err
		}
		out = toCarrierView(c)
		if len(events) == 0 {
			return nil
		}
		return s.saveCarrier(ctx, c, loaded, events)
	})
	return out, err
}

func (s *Service) saveCarrier(ctx context.Context, c *carrier.Carrier, loaded int64, events []carrier.Event) error {
	if err := s.d.Carriers.Save(ctx, c, loaded); err != nil {
		return err
	}
	return s.d.Events.PublishCarrierEvents(ctx, events...)
}

// loadCarrier reads a registered carrier, translating the port's miss into the
// domain's carrier.ErrCarrierNotFound (a 404 carrier-not-found, not an appointment's).
func (s *Service) loadCarrier(ctx context.Context, scac carrier.Scac, forUpdate bool) (*carrier.Carrier, error) {
	get := s.d.Carriers.Get
	if forUpdate {
		get = s.d.Carriers.GetForUpdate
	}
	c, err := get(ctx, scac)
	if errors.Is(err, ports.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", carrier.ErrCarrierNotFound, scac)
	}
	return c, err
}

// GetCarrier returns one registered carrier (GetCarrier).
func (s *Service) GetCarrier(ctx context.Context, rawSCAC string) (views.CarrierView, error) {
	scac, err := carrier.NewScac(rawSCAC)
	if err != nil {
		return views.CarrierView{}, err
	}
	c, err := s.loadCarrier(ctx, scac, false)
	if err != nil {
		return views.CarrierView{}, err
	}
	return toCarrierView(c), nil
}

// ListCarriers serves one keyset page of the registry (ListCarriers): at most
// limit carriers (0 means DefaultListLimit) ordered by SCAC, strictly after the
// opaque cursor ("" means the first page), optionally only those of one status
// ("" means any).
func (s *Service) ListCarriers(ctx context.Context, status carrier.Status, limit int, cursor string) (views.CarrierPage, error) {
	n, err := listLimit(limit)
	if err != nil {
		return views.CarrierPage{}, err
	}
	after, err := decodeCarrierCursor(cursor)
	if err != nil {
		return views.CarrierPage{}, err
	}
	rows, err := s.d.Carriers.ListPage(ctx, repository.CarrierFilter{Status: string(status)}, after, n+1)
	if err != nil {
		return views.CarrierPage{}, err
	}
	page := views.CarrierPage{Items: make([]views.CarrierView, 0, len(rows))}
	for i, c := range rows {
		if i == n {
			page.NextCursor = encodeCarrierCursor(rows[n-1])
			break
		}
		page.Items = append(page.Items, toCarrierView(c))
	}
	return page, nil
}

// carrierCursor is the opaque page token of ListCarriers: the SCAC of the last
// row of the previous page (the listing is ordered by it).
type carrierCursor struct {
	ID string `json:"i"`
}

func decodeCarrierCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	bad := fmt.Errorf("%w: cursor is not a valid page token", ErrInvalidListQuery)
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", bad
	}
	var p carrierCursor
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", bad
	}
	if _, err := carrier.NewScac(p.ID); err != nil {
		return "", bad
	}
	return p.ID, nil
}

func encodeCarrierCursor(c *carrier.Carrier) string {
	b, _ := json.Marshal(carrierCursor{ID: c.SCAC().String()})
	return base64.RawURLEncoding.EncodeToString(b)
}

// checkCarrierMayBook is the booking rule of the registry, applied only when
// the booking names a carrier SCAC: a registered, SUSPENDED carrier is refused
// (carrier.ErrCarrierSuspended); a registered ACTIVE one and a SCAC that is not
// in the registry are accepted exactly as before (the registry is opt-in, ADR
// 0012); a booking with no SCAC never reads it.
func (s *Service) checkCarrierMayBook(ctx context.Context, rawSCAC string) error {
	if rawSCAC == "" {
		return nil
	}
	scac, err := carrier.NewScac(rawSCAC)
	if err != nil {
		return err
	}
	c, err := s.d.Carriers.Get(ctx, scac)
	if errors.Is(err, ports.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.CheckCanBook()
}
