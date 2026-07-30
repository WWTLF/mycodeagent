// Package serverprobe implements service.ServerProbe over a stdlib HTTP client.
//
// It reads `GET http://localhost:<port>/v1/models` for the served model id, then
// falls back to llama.cpp's `GET /props` for the context window: llama-server's
// OpenAI-compatible model listing has no max_model_len field (that was a vLLM
// extension), but /props reports the real per-slot n_ctx. That number is the one
// callers want — it reflects both scaledContextLength and any downward
// adjustment llama.cpp made to fit the context into VRAM.
package serverprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/service"
)

type Probe struct {
	client *http.Client
}

var _ service.ServerProbe = (*Probe)(nil)

func New() *Probe {
	return &Probe{
		client: &http.Client{Timeout: 3 * time.Second},
	}
}

func (p *Probe) GetServedModel(ctx context.Context, localPort int) (string, int, error) {
	var models struct {
		Data []struct {
			ID string `json:"id"`
			// Present on vLLM, absent on llama.cpp — kept so an instance served by
			// something else still reports a context without the /props round trip.
			MaxModelLen int `json:"max_model_len"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, localPort, "/v1/models", &models); err != nil {
		// Best-effort probe — unreachable just means "no info", not an error.
		return "", 0, nil
	}
	if len(models.Data) == 0 {
		return "", 0, nil
	}

	id := models.Data[0].ID
	maxLen := models.Data[0].MaxModelLen
	if maxLen == 0 {
		maxLen = p.contextFromProps(ctx, localPort)
	}
	return id, maxLen, nil
}

// contextFromProps reads the per-slot context size llama-server actually loaded.
// Returns 0 when unavailable so callers fall back to the catalog value.
func (p *Probe) contextFromProps(ctx context.Context, localPort int) int {
	var props struct {
		DefaultGenerationSettings struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := p.getJSON(ctx, localPort, "/props", &props); err != nil {
		return 0
	}
	return props.DefaultGenerationSettings.NCtx
}

func (p *Probe) getJSON(ctx context.Context, localPort int, path string, out any) error {
	url := fmt.Sprintf("http://localhost:%d%s", localPort, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
