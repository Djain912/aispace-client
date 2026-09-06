package api

import "encoding/json"

// File mirrors the API File object.
type File struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256,omitempty"`
	EncAlg      string `json:"enc_alg,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

// ShareLink mirrors the API ShareLink object. URL is only set on creation.
type ShareLink struct {
	ID            string `json:"id"`
	FileID        string `json:"file_id"`
	URL           string `json:"url,omitempty"`
	ExpiresAt     int64  `json:"expires_at"`
	MaxDownloads  *int64 `json:"max_downloads"`
	DownloadCount int64  `json:"download_count"`
	CreatedAt     int64  `json:"created_at"`
	RevokedAt     *int64 `json:"revoked_at"`
}

// BotKey mirrors the API BotKey object.
type BotKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Prefix      string `json:"prefix"`
	BudgetBytes int64  `json:"budget_bytes"`
	UsedBytes   int64  `json:"used_bytes"`
	CreatedAt   int64  `json:"created_at"`
	LastUsedAt  *int64 `json:"last_used_at"`
	RevokedAt   *int64 `json:"revoked_at"`
}

// Whoami is the GET /v1/whoami response.
type Whoami struct {
	Key  BotKey `json:"key"`
	User struct {
		Email string `json:"email"`
	} `json:"user"`
}

// Quota is the GET /v1/quota response.
type Quota struct {
	Key struct {
		BudgetBytes    int64 `json:"budget_bytes"`
		UsedBytes      int64 `json:"used_bytes"`
		RemainingBytes int64 `json:"remaining_bytes"`
	} `json:"key"`
	Account struct {
		AllowanceBytes int64  `json:"allowance_bytes"`
		UsedBytes      int64  `json:"used_bytes"`
		RemainingBytes int64  `json:"remaining_bytes"`
		Plan           string `json:"plan"`
		ExtraBlocks    int64  `json:"extra_blocks"`
	} `json:"account"`
	Limits struct {
		MaxFileBytes      int64 `json:"max_file_bytes"`
		MaxFileTTLSeconds int64 `json:"max_file_ttl_seconds"`
		MaxLinkTTLSeconds int64 `json:"max_link_ttl_seconds"`
		UploadsPerHour    int64 `json:"uploads_per_hour"`
		UploadsPerDay     int64 `json:"uploads_per_day"`
		RequestsPerMinute int64 `json:"requests_per_minute"`
	} `json:"limits"`
	Rate struct {
		UploadsHourRemaining    int64 `json:"uploads_hour_remaining"`
		UploadsDayRemaining     int64 `json:"uploads_day_remaining"`
		RequestsMinuteRemaining int64 `json:"requests_minute_remaining"`
	} `json:"rate"`
}

// FileList is the GET /v1/files response.
type FileList struct {
	Files      []json.RawMessage `json:"files"`
	NextCursor *string           `json:"next_cursor"`
}

// Result pairs a decoded value with the raw response body so callers can
// print exactly what the API returned.
type Result[T any] struct {
	Value T
	Raw   json.RawMessage
}

// LinkList is the GET /v1/files/:id/links response.
type LinkList struct {
	Links []ShareLink `json:"links"`
}
