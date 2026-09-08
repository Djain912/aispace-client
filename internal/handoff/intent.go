// Package handoff provides strict, local-only parsing of transfer links,
// CLI tokens and native deep links into one canonical transfer intent.
package handoff

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"

	"github.com/aispace-sh/aispace-client/internal/sealed"
)

const (
	TokenPrefix = "aispace-transfer-v1."
	DeepScheme  = "aispace"
	maxInput    = 8192
	checksumLen = 8
)

type Mode string

const (
	ModeSealedLink Mode = "sealed-link"
	ModeAddressed  Mode = "addressed"
	ModeLive       Mode = "live"
)

// Intent is the canonical, versioned result shared by every handoff form.
type Intent struct {
	Version             int
	Mode                Mode
	ServiceOrigin       string
	TransferID          string
	MasterKey           []byte
	ClaimCapability     []byte
	SignalingCapability []byte
	IdentityID          string
	IdentityFingerprint string
	PairingCode         string
}

var rawURL = base64.RawURLEncoding

// FromSealed creates a v1 intent without retaining any input URL text.
func FromSealed(origin, transferID string, secrets sealed.Secrets) (Intent, error) {
	canonical, err := canonicalOrigin(origin)
	if err != nil {
		return Intent{}, err
	}
	intent := Intent{
		Version: 1, Mode: ModeSealedLink, ServiceOrigin: canonical, TransferID: transferID,
		MasterKey:       append([]byte(nil), secrets.MasterKey[:]...),
		ClaimCapability: append([]byte(nil), secrets.ClaimCapability[:]...),
	}
	return intent, intent.Validate()
}

func (i Intent) Validate() error {
	if i.Version != 1 {
		return errors.New("unsupported transfer intent version")
	}
	origin, err := canonicalOrigin(i.ServiceOrigin)
	if err != nil || origin != i.ServiceOrigin {
		return errors.New("transfer intent has an invalid service origin")
	}
	if len(i.TransferID) == 0 || len(i.TransferID) > 128 || strings.ContainsAny(i.TransferID, "/?#") {
		return errors.New("transfer intent has an invalid transfer ID")
	}
	switch i.Mode {
	case ModeSealedLink:
		if len(i.MasterKey) != 32 || len(i.ClaimCapability) != 32 || len(i.SignalingCapability) != 0 || i.IdentityID != "" || i.IdentityFingerprint != "" || i.PairingCode != "" {
			return errors.New("sealed-link intent has an invalid field combination")
		}
	case ModeAddressed:
		if len(i.MasterKey) != 0 || len(i.ClaimCapability) != 0 || i.IdentityID == "" || i.PairingCode != "" {
			return errors.New("addressed intent has an invalid field combination")
		}
	case ModeLive:
		if len(i.MasterKey) != 0 || len(i.ClaimCapability) != 0 || len(i.SignalingCapability) != 32 || normalizePairingCode(i.PairingCode) == "" {
			return errors.New("live intent has an invalid field combination")
		}
	default:
		return errors.New("transfer intent has an unknown mode")
	}
	return nil
}

func (i Intent) Token() (string, error) {
	payload, err := i.marshal()
	if err != nil {
		return "", err
	}
	return TokenPrefix + rawURL.EncodeToString(payload), nil
}

func (i Intent) DeepLink() (string, error) {
	payload, err := i.marshal()
	if err != nil {
		return "", err
	}
	return "aispace://transfer/v1/" + rawURL.EncodeToString(payload), nil
}

func (i Intent) HTTPSLink() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	if i.Mode != ModeSealedLink {
		return "", errors.New("only sealed-link intents have an HTTPS bearer representation")
	}
	var secrets sealed.Secrets
	copy(secrets.MasterKey[:], i.MasterKey)
	copy(secrets.ClaimCapability[:], i.ClaimCapability)
	return i.ServiceOrigin + "/t/" + url.PathEscape(i.TransferID) + "#" + secrets.Fragment(), nil
}

// Parse validates an input completely before callers make a network request.
// expectedOrigin prevents a deep link/token from redirecting credentials to a
// different service. Empty expectedOrigin accepts the embedded HTTPS origin.
func Parse(value, expectedOrigin string) (Intent, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxInput {
		return Intent{}, errors.New("transfer reference is empty or too long")
	}
	var intent Intent
	var err error
	switch {
	case strings.HasPrefix(value, TokenPrefix):
		intent, err = parsePayload(strings.TrimPrefix(value, TokenPrefix))
	case strings.HasPrefix(strings.ToLower(value), "aispace://"):
		intent, err = parseDeepLink(value)
	case strings.HasPrefix(value, sealed.TokenPrefix):
		if expectedOrigin == "" {
			return Intent{}, errors.New("legacy CLI tokens require a configured service origin")
		}
		id, secrets, parseErr := sealed.ParseReference(value)
		if parseErr != nil {
			return Intent{}, parseErr
		}
		intent, err = FromSealed(expectedOrigin, id, secrets)
	default:
		intent, err = parseHTTPS(value)
	}
	if err != nil {
		return Intent{}, err
	}
	if expectedOrigin != "" {
		canonical, originErr := canonicalOrigin(expectedOrigin)
		if originErr != nil || canonical != intent.ServiceOrigin {
			return Intent{}, errors.New("transfer reference origin does not match the configured service")
		}
	}
	return intent, nil
}

func parseHTTPS(value string) (Intent, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return Intent{}, errors.New("transfer link is malformed")
	}
	origin, err := canonicalOrigin(u.Scheme + "://" + u.Host)
	if err != nil {
		return Intent{}, err
	}
	clean := path.Clean(u.EscapedPath())
	if clean != u.EscapedPath() {
		return Intent{}, errors.New("transfer link path is not canonical")
	}
	parts := strings.Split(strings.Trim(clean, "/"), "/")
	if len(parts) != 2 || parts[0] != "t" {
		return Intent{}, errors.New("transfer link path must be /t/<transfer-id>")
	}
	id, err := url.PathUnescape(parts[1])
	if err != nil {
		return Intent{}, errors.New("transfer link has an invalid transfer ID")
	}
	secrets, err := sealed.ParseFragment(u.Fragment)
	if err != nil {
		return Intent{}, err
	}
	return FromSealed(origin, id, secrets)
}

func parseDeepLink(value string) (Intent, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != DeepScheme || u.Host != "transfer" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return Intent{}, errors.New("native transfer link is malformed")
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "v1" {
		return Intent{}, errors.New("native transfer link has an unsupported path")
	}
	payload, err := url.PathUnescape(parts[1])
	if err != nil {
		return Intent{}, errors.New("native transfer link payload is malformed")
	}
	return parsePayload(payload)
}

func (i Intent) marshal() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("ASHT")
	b.WriteByte(1)
	b.WriteByte(modeByte(i.Mode))
	fields := []struct {
		tag  byte
		data []byte
	}{
		{1, []byte(i.ServiceOrigin)}, {2, []byte(i.TransferID)}, {3, i.MasterKey}, {4, i.ClaimCapability},
		{5, i.SignalingCapability}, {6, []byte(i.IdentityID)}, {7, []byte(i.IdentityFingerprint)}, {8, []byte(i.PairingCode)},
	}
	for _, field := range fields {
		if len(field.data) == 0 {
			continue
		}
		if len(field.data) > 65535 {
			return nil, errors.New("transfer intent field is too long")
		}
		b.WriteByte(field.tag)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(field.data)))
		b.Write(field.data)
	}
	digest := sha256.Sum256(b.Bytes())
	b.Write(digest[:checksumLen])
	if b.Len() > maxInput {
		return nil, errors.New("transfer intent is too long")
	}
	return b.Bytes(), nil
}

func parsePayload(encoded string) (Intent, error) {
	data, err := rawURL.DecodeString(encoded)
	if err != nil || rawURL.EncodeToString(data) != encoded || len(data) < 6+checksumLen || len(data) > maxInput {
		return Intent{}, errors.New("transfer token payload is malformed")
	}
	body, checksum := data[:len(data)-checksumLen], data[len(data)-checksumLen:]
	digest := sha256.Sum256(body)
	if subtle.ConstantTimeCompare(checksum, digest[:checksumLen]) != 1 {
		return Intent{}, errors.New("transfer token checksum does not match")
	}
	if string(body[:4]) != "ASHT" || body[4] != 1 {
		return Intent{}, errors.New("unsupported transfer token version")
	}
	intent := Intent{Version: 1, Mode: byteMode(body[5])}
	if intent.Mode == "" {
		return Intent{}, errors.New("transfer token mode is invalid")
	}
	seen := [9]bool{}
	for cursor := 6; cursor < len(body); {
		if cursor+3 > len(body) {
			return Intent{}, errors.New("transfer token field is truncated")
		}
		tag, size := body[cursor], int(binary.BigEndian.Uint16(body[cursor+1:cursor+3]))
		cursor += 3
		if tag == 0 || tag > 8 || seen[tag] || cursor+size > len(body) || size == 0 {
			return Intent{}, errors.New("transfer token contains duplicate, unknown, or truncated fields")
		}
		seen[tag] = true
		field := append([]byte(nil), body[cursor:cursor+size]...)
		cursor += size
		switch tag {
		case 1:
			intent.ServiceOrigin = string(field)
		case 2:
			intent.TransferID = string(field)
		case 3:
			intent.MasterKey = field
		case 4:
			intent.ClaimCapability = field
		case 5:
			intent.SignalingCapability = field
		case 6:
			intent.IdentityID = string(field)
		case 7:
			intent.IdentityFingerprint = string(field)
		case 8:
			intent.PairingCode = string(field)
		}
	}
	if err := intent.Validate(); err != nil {
		return Intent{}, err
	}
	canonical, err := intent.marshal()
	if err != nil || !bytes.Equal(canonical, data) {
		return Intent{}, errors.New("transfer token is not canonical")
	}
	return intent, nil
}

func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("service origin is malformed")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" {
		ip := net.ParseIP(host)
		if u.Scheme != "http" || !(host == "localhost" || strings.HasSuffix(host, ".localhost") || ip != nil && ip.IsLoopback()) {
			return "", errors.New("service origin must use HTTPS")
		}
	}
	if u.Scheme == "https" && u.Port() == "443" || u.Scheme == "http" && u.Port() == "80" {
		u.Host = originHost(host)
	} else if u.Port() != "" {
		u.Host = net.JoinHostPort(host, u.Port())
	} else {
		u.Host = originHost(host)
	}
	u.Path = ""
	return u.Scheme + "://" + u.Host, nil
}

func originHost(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// CanonicalOrigin normalizes a configured service origin for safe equality
// checks without accepting credentials, paths, or insecure remote HTTP.
func CanonicalOrigin(raw string) (string, error) { return canonicalOrigin(raw) }

func modeByte(mode Mode) byte {
	if mode == ModeSealedLink {
		return 1
	}
	if mode == ModeAddressed {
		return 2
	}
	if mode == ModeLive {
		return 3
	}
	return 0
}
func byteMode(value byte) Mode {
	if value == 1 {
		return ModeSealedLink
	}
	if value == 2 {
		return ModeAddressed
	}
	if value == 3 {
		return ModeLive
	}
	return ""
}

func normalizePairingCode(value string) string {
	n := strings.ToUpper(strings.ReplaceAll(value, "-", ""))
	if len(n) != 8 {
		return ""
	}
	for _, r := range n {
		if !strings.ContainsRune("23456789ABCDEFGHJKMNPQRSTUVWXYZ", r) {
			return ""
		}
	}
	return n[:4] + "-" + n[4:]
}

func (i Intent) String() string {
	return fmt.Sprintf("v%d %s transfer %s at %s", i.Version, i.Mode, i.TransferID, i.ServiceOrigin)
}
