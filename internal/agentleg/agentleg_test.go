package agentleg

import "testing"

func TestBuild(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			// Orchestrator inbound transfer: attribute on, auto-answer off.
			name: "gateway attribute (orchestrator transfer)",
			opts: Options{Transport: "gateway", Gateway: "tring-gw", SIP: "alice", TenantID: "t1", Attribute: true},
			want: "[tring_agent=alice,sip_h_X-Tring-Tenant=t1,sip_h_X-Tring-Agent-Sip=alice]sofia/gateway/tring-gw/alice",
		},
		{
			// API click-to-call: auto-answer on, attribute off.
			name: "gateway auto-answer (api click-to-call)",
			opts: Options{Transport: "gateway", Gateway: "tring-gw", SIP: "alice", TenantID: "t1", AutoAnswer: true},
			want: "[sip_h_X-Tring-Tenant=t1,sip_h_X-Tring-Agent-Sip=alice,sip_h_X-Tring-Auto-Answer=1]sofia/gateway/tring-gw/alice",
		},
		{
			name: "gateway with sanitized label",
			opts: Options{Transport: "gateway", Gateway: "tring-gw", SIP: "alice", TenantID: "t1", Attribute: true, Label: "Acme, [Corp]=x"},
			want: "[tring_agent=alice,sip_h_X-Tring-Tenant=t1,sip_h_X-Tring-Agent-Sip=alice,sip_h_X-Tring-Agent-Label=Acme Corpx]sofia/gateway/tring-gw/alice",
		},
		{
			// Email-style username: the @ must NOT reach the request-URI user (it
			// would make FS misroute to the @domain), so the routing user is the
			// local-part while the full username rides X-Tring-Agent-Sip.
			name: "gateway email username routes on local-part",
			opts: Options{Transport: "gateway", Gateway: "tring-gw", SIP: "kalpesh@zerodha.com", TenantID: "t1", Attribute: true},
			want: "[tring_agent=kalpesh@zerodha.com,sip_h_X-Tring-Tenant=t1,sip_h_X-Tring-Agent-Sip=kalpesh@zerodha.com]sofia/gateway/tring-gw/kalpesh",
		},
		{
			name: "sip transport with attribution (orchestrator)",
			opts: Options{Transport: "sip", SIP: "alice", Domain: "d.tring.local", Attribute: true},
			want: "[tring_agent=alice]user/alice@d.tring.local",
		},
		{
			name: "sip transport plain (api)",
			opts: Options{Transport: "sip", SIP: "alice", Domain: "d.tring.local"},
			want: "user/alice@d.tring.local",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Build(tt.opts); got != tt.want {
				t.Errorf("Build() = %q, want %q", got, tt.want)
			}
		})
	}
}
