package adaptive

import "testing"

func TestValidateRejectsUnimplementedModesAndPolicies(t *testing.T) {
	valid := Request{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: PrivacyRelayOnly}
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Request{
		{Mode: "live-only", Durability: DurableFirst, Privacy: PrivacyDirect},
		{Mode: ModeAdaptive, Durability: "direct-first", Privacy: PrivacyDirect},
		{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: "hidden"},
	} {
		if err := Validate(invalid); err == nil {
			t.Fatalf("accepted invalid request: %+v", invalid)
		}
	}
}

func TestSelectionRejectsInvalidRequestsBeforeConsideringLiveCapabilities(t *testing.T) {
	service := ServiceCapabilities{
		Protocol: Protocol, Enabled: true, DefaultMode: ModeStored, NegotiationBudgetMS: 2000,
		SupportedModes: []string{ModeStored, ModeAdaptive}, DurabilityPolicies: []string{DurableFirstWire},
		PrivacyModes: []string{"stored_only", "relay_only", "direct"},
		Transports: TransportCapabilities{
			R2:     Capability{Available: true, Durable: true},
			Direct: Capability{Available: true, ExposesPeerAddress: true, Capability: WebRTCDataChannel},
			TURN:   Capability{Available: true, Capability: WebRTCDataChannel},
		},
	}
	for _, request := range []Request{
		{Mode: "live-only", Durability: DurableFirst, Privacy: PrivacyDirect},
		{Mode: ModeAdaptive, Durability: "direct-first", Privacy: PrivacyDirect},
		{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: "hidden"},
	} {
		got := Select(request, service, []string{WebRTCDataChannel})
		if got.Transport != TransportR2 || !got.Fallback || got.Reason != "invalid_request" {
			t.Fatalf("invalid request selected live transport: request=%+v selection=%+v", request, got)
		}
	}
}

func TestSelectionHonorsPrivacyAndFallsBack(t *testing.T) {
	service := ServiceCapabilities{
		Protocol: Protocol, Enabled: true, DefaultMode: ModeStored, NegotiationBudgetMS: 2000, SupportedModes: []string{ModeStored, ModeAdaptive},
		DurabilityPolicies: []string{"durable_first"}, PrivacyModes: []string{"stored_only", "relay_only", "direct"},
		Transports: TransportCapabilities{
			R2:     Capability{Available: true, Durable: true},
			Direct: Capability{Available: true, ExposesPeerAddress: true, Capability: WebRTCDataChannel},
			TURN:   Capability{Available: true, Capability: WebRTCDataChannel},
		},
	}
	client := []string{WebRTCDataChannel}
	tests := []struct {
		name      string
		request   Request
		client    []string
		transport string
		fallback  bool
	}{
		{"stored", Request{Mode: ModeStored, Durability: DurableFirst, Privacy: PrivacyDirect}, client, TransportR2, false},
		{"stored-only", Request{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: PrivacyStoredOnly}, client, TransportR2, true},
		{"direct", Request{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: PrivacyDirect}, client, TransportDirect, false},
		{"relay-only", Request{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: PrivacyRelayOnly}, client, TransportTURN, false},
		{"unsupported client", Request{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: PrivacyDirect}, nil, TransportR2, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Select(test.request, service, test.client)
			if got.Transport != test.transport || got.Fallback != test.fallback {
				t.Fatalf("selection = %+v", got)
			}
		})
	}
}

func TestRelayOnlyNeverSelectsDirect(t *testing.T) {
	service := ServiceCapabilities{Protocol: Protocol, Enabled: true, DefaultMode: ModeStored, NegotiationBudgetMS: 2000, SupportedModes: []string{ModeStored, ModeAdaptive}, DurabilityPolicies: []string{"durable_first"}, PrivacyModes: []string{"relay_only"}}
	service.Transports.R2 = Capability{Available: true, Durable: true}
	service.Transports.Direct = Capability{Available: true, ExposesPeerAddress: true, Capability: WebRTCDataChannel}
	got := Select(Request{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: PrivacyRelayOnly}, service, []string{WebRTCDataChannel})
	if got.Transport != TransportR2 || !got.Fallback {
		t.Fatalf("relay-only leaked to direct: %+v", got)
	}
}

func TestSelectionRejectsUnadvertisedPoliciesAndMalformedCapabilities(t *testing.T) {
	base := ServiceCapabilities{
		Protocol: Protocol, Enabled: true, DefaultMode: ModeStored, NegotiationBudgetMS: 2000, SupportedModes: []string{ModeStored, ModeAdaptive},
		DurabilityPolicies: []string{"durable_first"}, PrivacyModes: []string{"relay_only"},
		Transports: TransportCapabilities{
			R2:   Capability{Available: true, Durable: true},
			TURN: Capability{Available: true, Capability: WebRTCDataChannel},
		},
	}
	request := Request{Mode: ModeAdaptive, Durability: DurableFirst, Privacy: PrivacyRelayOnly}
	client := []string{WebRTCDataChannel}
	for name, mutate := range map[string]func(*ServiceCapabilities){
		"durability missing":    func(s *ServiceCapabilities) { s.DurabilityPolicies = nil },
		"privacy missing":       func(s *ServiceCapabilities) { s.PrivacyModes = nil },
		"r2 not durable":        func(s *ServiceCapabilities) { s.Transports.R2.Durable = false },
		"relay exposes address": func(s *ServiceCapabilities) { s.Transports.TURN.ExposesPeerAddress = true },
		"unknown capability":    func(s *ServiceCapabilities) { s.Transports.TURN.Capability = "unknown-v1" },
		"candidate logging":     func(s *ServiceCapabilities) { s.CandidatePayloadsLog = true },
		"missing budget":        func(s *ServiceCapabilities) { s.NegotiationBudgetMS = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			service := base
			mutate(&service)
			got := Select(request, service, client)
			if got.Transport != TransportR2 || !got.Fallback {
				t.Fatalf("malformed service selected live transport: %+v", got)
			}
		})
	}
}
