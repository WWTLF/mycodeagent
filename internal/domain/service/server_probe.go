package service

import "context"

// ServerProbe abstracts the localhost OpenAI-compatible server probe used to ask
// a running model server which model is loaded and what its context window is.
// The domain layer needs this to (a) detect served model IDs for opencode config
// writes and (b) report context length back to clients without trusting the
// catalog default.
//
// Implementations live under internal/infrastructure/serverprobe/.
type ServerProbe interface {
	// GetServedModel queries the local server and returns the first model's id
	// and its context window. Returns ("", 0, nil) — not an error — if the server
	// is unreachable or still loading, since this is a best-effort probe.
	// Returns an error only on context cancellation or unexpected decode failure.
	GetServedModel(ctx context.Context, localPort int) (id string, maxModelLen int, err error)
}
