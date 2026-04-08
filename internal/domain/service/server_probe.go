package service

import "context"

// ServerProbe abstracts the localhost OpenAI-compatible server probe used to ask
// a running model server (vLLM or LM Studio) which model is loaded and what its
// max context length is. The domain layer needs this to (a) detect served model
// IDs for opencode config writes and (b) report context length back to clients
// without trusting the catalog default.
//
// Implementations live under internal/infrastructure/serverprobe/.
type ServerProbe interface {
	// GetServedModel queries http://localhost:<localPort>/v1/models and returns
	// the first model's id and max_model_len. Returns ("", 0, nil) — not an
	// error — if the server is unreachable, since this is a best-effort probe.
	// Returns an error only on context cancellation or unexpected decode failure.
	GetServedModel(ctx context.Context, localPort int) (id string, maxModelLen int, err error)
}
