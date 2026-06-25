// Package room is the transport-agnostic core of Tring's pion media plane: a
// call is a Room with N Participants, and a Participant's identity and lifetime
// are decoupled from its transport (the WebRTC PeerConnection / signaling
// socket, which live in internal/media/sfu and internal/media/signaling).
//
// The room cannot tell a browser participant from an externally-fed one — that
// is the whole point. The IVR (a prompt participant, and later an AI voice agent)
// joins as an ordinary participant that publishes prompt audio and consumes the caller's
// audio + DTMF; the phone leg (FreeSWITCH or other PSTN adapter, behind
// Plug) joins as an Opus ParticipantSource; routing a call to a human is
// just a membership change. See docs/ARCHITECTURE.md.
//
// Milestone 1 (this package) builds the model only: the participant state
// machine (joining → connected → reconnecting → left), the grace window that
// lets a participant survive a page refresh, event emission for the signaling
// layer to fan out, and the four extension interfaces (TrackSink,
// ParticipantSource, Supervisor, Plug) as the seams for later phases.
// No pion/webrtc, no network — the clock is injected so the grace-window logic
// is unit-testable without sleeping.
package room
