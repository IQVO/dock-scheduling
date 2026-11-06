package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mimeTypeJSON is the MIME type of every resource this server serves.
const mimeTypeJSON = "application/json"

// The resource URI templates. Resources are SCOPED read models this context
// owns (one site's calendar, one appointment, one site's at-risk list), never
// bulk dumps; each is backed by the same use case as the matching tool and the
// REST route.
const (
	calendarResourceTemplate    = "dock://sites/{siteId}/calendar"
	atRiskResourceTemplate      = "dock://sites/{siteId}/at-risk"
	appointmentResourceTemplate = "dock://appointments/{appointmentId}"
)

// registerResources adds the scoped read-model resources. Resources are L1 by
// nature (pure reads) and, per the charter, need no tool Meta.
func (d Deps) registerResources(server *mcp.Server) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: calendarResourceTemplate,
		Name:        "dock calendar",
		Description: "One site's door calendar with defaults applied: booking settings (max window, slot granularity, arrival tolerance, no-show grace), door operating hours and active closures.",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		siteID, err := resourceSegment(req.Params.URI, "dock://sites/", "/calendar", calendarResourceTemplate)
		if err != nil {
			return nil, err
		}
		v, err := d.Service.GetDockCalendar(ctx, siteID)
		if err != nil {
			return nil, mapError(err)
		}
		return jsonResource(req.Params.URI, toCalendar(v))
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: atRiskResourceTemplate,
		Name:        "at-risk appointments",
		Description: "The first page (100) of one site's BOOKED appointments flagged at risk: a late ETA or a door overlap. Advisory; page further with list_at_risk_appointments.",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		siteID, err := resourceSegment(req.Params.URI, "dock://sites/", "/at-risk", atRiskResourceTemplate)
		if err != nil {
			return nil, err
		}
		page, err := d.Service.ListAtRisk(ctx, siteID, 0, "")
		if err != nil {
			return nil, mapError(err)
		}
		return jsonResource(req.Params.URI, toAppointmentPage(page))
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: appointmentResourceTemplate,
		Name:        "appointment",
		Description: "One dock appointment by id: door, window, carrier, references, state, latest ETA, arrival, risk flag and version.",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id, err := resourceSegment(req.Params.URI, "dock://appointments/", "", appointmentResourceTemplate)
		if err != nil {
			return nil, err
		}
		v, err := d.Service.GetAppointment(ctx, id)
		if err != nil {
			return nil, mapError(err)
		}
		return jsonResource(req.Params.URI, toAppointment(v))
	})
}

// resourceSegment extracts the one model-supplied segment between prefix and
// suffix. The segment is untrusted: a malformed URI, an empty segment or one
// holding a slash is rejected rather than defaulted.
func resourceSegment(uri, prefix, suffix, template string) (string, error) {
	rest, ok := strings.CutPrefix(uri, prefix)
	if ok && suffix != "" {
		rest, ok = strings.CutSuffix(rest, suffix)
	}
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", fmt.Errorf("resource uri %q must match %s", uri, template)
	}
	return rest, nil
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: mimeTypeJSON, Text: string(body)}},
	}, nil
}
