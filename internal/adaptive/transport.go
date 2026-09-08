// Package adaptive contains transport selection policy. It never handles
// plaintext: every byte-plane implementation must carry aispace-sealed-v1
// manifest and chunk ciphertext unchanged.
package adaptive

import (
	"fmt"
	"slices"
)

const (
	Protocol          = "aispace-adaptive-v1"
	ModeStored        = "stored"
	ModeAdaptive      = "adaptive"
	DurableFirst      = "durable-first"
	DurableFirstWire  = "durable_first"
	PrivacyStoredOnly = "stored-only"
	PrivacyRelayOnly  = "relay-only"
	PrivacyDirect     = "direct"
	TransportR2       = "r2"
	TransportDirect   = "direct"
	TransportTURN     = "turn"
	WebRTCDataChannel = "webrtc-datachannel-v1"
)

type ServiceCapabilities struct {
	Protocol             string                `json:"protocol"`
	Enabled              bool                  `json:"enabled"`
	DefaultMode          string                `json:"default_mode"`
	SupportedModes       []string              `json:"supported_modes"`
	DurabilityPolicies   []string              `json:"durability_policies"`
	NegotiationBudgetMS  int                   `json:"negotiation_budget_ms"`
	CandidatePayloadsLog bool                  `json:"candidate_payloads_logged"`
	Transports           TransportCapabilities `json:"transports"`
	PrivacyModes         []string              `json:"privacy_modes"`
}

type TransportCapabilities struct {
	R2          Capability `json:"r2"`
	Direct      Capability `json:"direct"`
	TURN        Capability `json:"turn"`
	NativeRelay Capability `json:"native_relay"`
}

type Capability struct {
	Available          bool   `json:"available"`
	Durable            bool   `json:"durable"`
	ExposesPeerAddress bool   `json:"exposes_peer_address"`
	Capability         string `json:"capability,omitempty"`
}

type Request struct {
	Mode       string
	Durability string
	Privacy    string
}

type Selection struct {
	RequestedMode string
	Transport     string
	Fallback      bool
	Reason        string
}

func Validate(request Request) error {
	if request.Mode != ModeStored && request.Mode != ModeAdaptive {
		return fmt.Errorf("transport must be %q or %q", ModeStored, ModeAdaptive)
	}
	if request.Durability != DurableFirst {
		return fmt.Errorf("only %q durability is supported", DurableFirst)
	}
	if request.Privacy != PrivacyStoredOnly && request.Privacy != PrivacyRelayOnly && request.Privacy != PrivacyDirect {
		return fmt.Errorf("privacy must be %q, %q, or %q", PrivacyStoredOnly, PrivacyRelayOnly, PrivacyDirect)
	}
	return nil
}

// Select returns the first policy-permitted live candidate. Callers must still
// impose the service negotiation budget and fall back to the unchanged R2
// upload on every live setup or transfer failure.
func Select(request Request, service ServiceCapabilities, clientCapabilities []string) Selection {
	if err := Validate(request); err != nil {
		return Selection{RequestedMode: request.Mode, Transport: TransportR2, Fallback: true, Reason: "invalid_request"}
	}
	if request.Mode == ModeStored {
		return Selection{RequestedMode: request.Mode, Transport: TransportR2, Reason: "stored_requested"}
	}
	if !serviceSupportsRequest(request, service) {
		if !service.Enabled || service.Protocol != Protocol || !slices.Contains(service.SupportedModes, ModeAdaptive) {
			return Selection{RequestedMode: request.Mode, Transport: TransportR2, Fallback: true, Reason: "service_unavailable"}
		}
		return Selection{RequestedMode: request.Mode, Transport: TransportR2, Fallback: true, Reason: "invalid_service_capabilities"}
	}
	if request.Privacy == PrivacyStoredOnly {
		return Selection{RequestedMode: request.Mode, Transport: TransportR2, Fallback: true, Reason: "stored_only_policy"}
	}
	if request.Privacy == PrivacyDirect && compatible(service.Transports.Direct, clientCapabilities) {
		return Selection{RequestedMode: request.Mode, Transport: TransportDirect}
	}
	if compatible(service.Transports.TURN, clientCapabilities) && (request.Privacy != PrivacyRelayOnly || !service.Transports.TURN.ExposesPeerAddress) {
		return Selection{RequestedMode: request.Mode, Transport: TransportTURN}
	}
	return Selection{RequestedMode: request.Mode, Transport: TransportR2, Fallback: true, Reason: "client_or_path_unsupported"}
}

// SupportsRequest reports whether a discovery document safely advertises the
// requested adaptive intent. Selection can still fall back to R2 when no live
// byte-plane capability is available locally.
func SupportsRequest(request Request, service ServiceCapabilities) bool {
	return Validate(request) == nil && request.Mode == ModeAdaptive && serviceSupportsRequest(request, service)
}

func serviceSupportsRequest(request Request, service ServiceCapabilities) bool {
	if !service.Enabled || service.Protocol != Protocol || !slices.Contains(service.SupportedModes, ModeAdaptive) {
		return false
	}
	return validServicePolicy(request, service)
}

func compatible(capability Capability, client []string) bool {
	return capability.Available && !capability.Durable && capability.Capability == WebRTCDataChannel && slices.Contains(client, capability.Capability)
}

func validServicePolicy(request Request, service ServiceCapabilities) bool {
	if service.DefaultMode != ModeStored || service.NegotiationBudgetMS <= 0 || service.NegotiationBudgetMS > 60_000 || service.CandidatePayloadsLog {
		return false
	}
	if !slices.Contains(service.SupportedModes, ModeStored) {
		return false
	}
	if !service.Transports.R2.Available || !service.Transports.R2.Durable || service.Transports.R2.ExposesPeerAddress {
		return false
	}
	if !slices.Contains(service.DurabilityPolicies, DurableFirstWire) || !slices.Contains(service.PrivacyModes, wirePrivacy(request.Privacy)) {
		return false
	}
	if service.Transports.Direct.Available && (service.Transports.Direct.Durable || !service.Transports.Direct.ExposesPeerAddress || service.Transports.Direct.Capability != WebRTCDataChannel) {
		return false
	}
	if service.Transports.TURN.Available && (service.Transports.TURN.Durable || service.Transports.TURN.ExposesPeerAddress || service.Transports.TURN.Capability != WebRTCDataChannel) {
		return false
	}
	return true
}

func wirePrivacy(privacy string) string {
	return map[string]string{PrivacyStoredOnly: "stored_only", PrivacyRelayOnly: "relay_only", PrivacyDirect: "direct"}[privacy]
}
