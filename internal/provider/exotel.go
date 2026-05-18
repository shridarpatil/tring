package provider

// Exotel — Indian CPaaS, supports the 1600 transactional series.
// See indianSIPTrunk for the shared behaviour; see docs/PHASE_1_DAY_9_EXOTEL.md
// and .env.example for activation steps.
type Exotel struct{ indianSIPTrunk }

func newExotel() Exotel { return Exotel{indianSIPTrunk{name: "exotel"}} }
