// Package serverprobe implements service.ServerProbe over a stdlib HTTP client.
//
// It's a thin wrapper around `GET http://localhost:<port>/v1/models`. vLLM
// includes max_model_len as a top-level field on each model object; LM Studio
// omits it (in which case maxModelLen returns 0 and callers should fall back
// to the catalog).
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
	url := fmt.Sprintf("http://localhost:%d/v1/models", localPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// Best-effort probe — unreachable just means "no info", not an error.
		return "", 0, nil
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			MaxModelLen int    `json:"max_model_len"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, nil
	}
	if len(result.Data) == 0 {
		return "", 0, nil
	}
	return result.Data[0].ID, result.Data[0].MaxModelLen, nil
}
