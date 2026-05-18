package provider

// Tata — Tata Tele Business Services SIP trunk. TATA_* env vars and
// provider="tata".
type Tata struct{ indianSIPTrunk }

func newTata() Tata { return Tata{indianSIPTrunk{name: "tata"}} }
