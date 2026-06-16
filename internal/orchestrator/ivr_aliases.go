package orchestrator

import "github.com/whatomate/tring/internal/ivr"

// The IVR flow model + pure helpers live in internal/ivr (transport-agnostic,
// shared with the pion driver so the FreeSWITCH executor and the web IVR can't
// diverge). These aliases keep the orchestrator's existing local names working
// unchanged — only the definitions moved. See docs/ARCHITECTURE.md (M7).

type (
	ivrNode     = ivr.Node
	ivrFlowData = ivr.Flow
	// SimStep is the public trace row (consumed by the API). Aliased to ivr.Step.
	SimStep = ivr.Step
)

const (
	nodeStart        = ivr.NodeStart
	nodeGreeting     = ivr.NodeGreeting
	nodeMenu         = ivr.NodeMenu
	nodeGather       = ivr.NodeGather
	nodeHTTPCallback = ivr.NodeHTTPCallback
	nodeTransfer     = ivr.NodeTransfer
	nodeGotoFlow     = ivr.NodeGotoFlow
	nodeTiming       = ivr.NodeTiming
	nodeHangup       = ivr.NodeHangup
)

var (
	parseFlowDef      = ivr.Parse
	findNode          = ivr.FindNode
	intFromConfig     = ivr.IntFromConfig
	stringFromConfig  = ivr.StringFromConfig
	boolFromConfig    = ivr.BoolFromConfig
	mapFromConfig     = ivr.MapFromConfig
	substituteVars    = ivr.SubstituteVars
	evalTimingOutcome = ivr.EvalTimingOutcome
)
