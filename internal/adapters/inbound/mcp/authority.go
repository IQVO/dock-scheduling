package mcp

import "github.com/modelcontextprotocol/go-sdk/mcp"

// Authority levels (Agentic Standard v2, AS-1; fleet rule
// .claude/rules/fleet/agent-authority.md). Every registered tool declares
// exactly one of these in its Meta under authorityMetaKey; the fitness tests in
// internal/architecture read the declarations statically.
const (
	// authorityMetaKey is the tools/list _meta key carrying a tool's level.
	authorityMetaKey = "com.warehouse/authority"
	// authorityL1 reads facts and changes nothing.
	authorityL1 = "L1"
	// authorityL3 changes state; a human confirmation sits between the proposal
	// and the call, held by the client, and the change is attributable.
	authorityL3 = "L3"
)

// authorityMeta is the Meta a tool registers to declare its authority level.
func authorityMeta(level string) mcp.Meta { return mcp.Meta{authorityMetaKey: level} }
