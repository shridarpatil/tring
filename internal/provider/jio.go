package provider

// Jio — Reliance Jio enterprise SIP trunk. Same activation flow as Airtel,
// with JIO_* env vars and provider="jio".
type Jio struct{ indianSIPTrunk }

func newJio() Jio { return Jio{indianSIPTrunk{name: "jio"}} }
