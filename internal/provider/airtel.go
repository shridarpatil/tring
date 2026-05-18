package provider

// Airtel — Bharti Airtel enterprise SIP trunk. Activation:
//   1. Fill AIRTEL_SIP_USERNAME / _PASSWORD / _REALM in .env.
//   2. Set AIRTEL_REGISTER=true.
//   3. docker compose up -d --force-recreate freeswitch.
//   4. POST /v1/numbers with provider="airtel" and your allocated DID.
type Airtel struct{ indianSIPTrunk }

func newAirtel() Airtel { return Airtel{indianSIPTrunk{name: "airtel"}} }
