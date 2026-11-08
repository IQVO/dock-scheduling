package kafka

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// The environment variables the consumer groups come from (ADR 0003). A group
// is never a literal: a missing one is a boot error naming the variable. The
// legacy importer's group is the one exception: unset means the importer is
// OFF.
const (
	EnvYardGroup         = "YARD_CONSUMER_GROUP"
	EnvFacilityGroup     = "FACILITY_CONSUMER_GROUP"
	EnvAsnGroup          = "ASN_CONSUMER_GROUP"
	EnvCptGroup          = "CPT_CONSUMER_GROUP"
	EnvLegacyImportGroup = "DOCK_LEGACY_IMPORT_CONSUMER_GROUP"
)

// Groups are the consumer group ids of the five consumers.
type Groups struct {
	Yard, Facility, Asn, Cpt string
	// LegacyImport is optional: "" keeps the strangler stage A importer off.
	LegacyImport string
}

// GroupsFromEnv reads the groups through getenv (os.Getenv at the composition
// root). Every missing mandatory variable is named in ONE error, so a boot
// failure lists all of them.
func GroupsFromEnv(getenv func(string) string) (Groups, error) {
	g := Groups{
		Yard: strings.TrimSpace(getenv(EnvYardGroup)), Facility: strings.TrimSpace(getenv(EnvFacilityGroup)),
		Asn: strings.TrimSpace(getenv(EnvAsnGroup)), Cpt: strings.TrimSpace(getenv(EnvCptGroup)),
		LegacyImport: strings.TrimSpace(getenv(EnvLegacyImportGroup)),
	}
	var missing []string
	for _, f := range []struct{ env, value string }{
		{EnvYardGroup, g.Yard}, {EnvFacilityGroup, g.Facility}, {EnvAsnGroup, g.Asn}, {EnvCptGroup, g.Cpt},
	} {
		if f.value == "" {
			missing = append(missing, f.env)
		}
	}
	if len(missing) > 0 {
		return Groups{}, fmt.Errorf("%w: set %s", ErrNoConsumerGroup, strings.Join(missing, ", "))
	}
	return g, nil
}

// UseCases is every use case the consumers drive; *usecases.Service
// implements it.
type UseCases interface {
	YardUseCases
	FacilityUseCases
	AsnUseCases
	CutoffUseCases
	LegacyUseCases
}

// Build constructs every consumer the groups enable: yard, facility, asn and
// cutoff always, the legacy importer only when its group is set. Nothing dials
// until Start.
func Build(tr Transport, g Groups, uc UseCases) ([]*Consumer, error) {
	var out []*Consumer
	fail := func(err error) ([]*Consumer, error) {
		for _, c := range out {
			_ = c.Close()
		}
		return nil, err
	}
	for _, build := range []func() (*Consumer, error){
		func() (*Consumer, error) { return NewYardConsumer(tr, g.Yard, uc) },
		func() (*Consumer, error) { return NewFacilityConsumer(tr, g.Facility, uc) },
		func() (*Consumer, error) { return NewAsnConsumer(tr, g.Asn, uc) },
		func() (*Consumer, error) { return NewCutoffConsumer(tr, g.Cpt, uc) },
		func() (*Consumer, error) { return NewLegacyImportConsumer(tr, g.LegacyImport, uc) },
	} {
		c, err := build()
		if err != nil {
			return fail(err)
		}
		if c != nil {
			out = append(out, c)
		}
	}
	return out, nil
}

// Running is a started set of consumers.
type Running struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// Start runs every non-nil consumer in its own goroutine and returns at once.
// The first consumer to stop on its own (a reader failure; never a message)
// cancels the others. ctx cancellation is the graceful shutdown: each consumer
// stops fetching, finishes nothing half-committed (an unhandled message stays
// uncommitted) and the consumers are closed. Wait returns when all stopped.
func Start(ctx context.Context, consumers ...*Consumer) *Running {
	ctx, cancel := context.WithCancel(ctx)
	r := &Running{cancel: cancel, done: make(chan struct{})}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, c := range consumers {
		if c == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := c.Run(ctx)
			if closeErr := c.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
				mu.Unlock()
			}
			cancel()
		}()
	}
	go func() {
		wg.Wait()
		r.err = errors.Join(errs...)
		close(r.done)
	}()
	return r
}

// Stop cancels every consumer and waits for them.
func (r *Running) Stop() error {
	r.cancel()
	return r.Wait()
}

// Wait blocks until every consumer stopped and returns their joined errors
// (context cancellation is not an error).
func (r *Running) Wait() error {
	<-r.done
	return r.err
}
