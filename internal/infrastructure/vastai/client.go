package vastai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const baseURL = "https://console.vast.ai"

// minInetDownMbps is the minimum host download bandwidth an offer must report.
//
// This exists because the deploy sorts purely by price, and the cheapest offers
// cluster in regions with poor reachability to HuggingFace. Three consecutive
// `rude` deploys landed on Sichuan machines advertising 82-284 Mbit/s and pulled
// the GGUF at ~1 MiB/s — a 21 GB model would have taken five hours, so each one
// died on the startup deadline. Parallel connections did not help: eight streams
// summed to the same throughput as one, so it is a hard cap, not per-connection
// throttling.
//
// 400 Mbit/s is comfortably above the 25 MB/s (200 Mbit/s) the timeout budget in
// model_repo_static.go assumes, and it leaves a wide pool: at the 32 GB tier it
// drops the three cheapest and keeps offers from ~$0.30/hr. Paying ~50% more per
// hour for a deploy that finishes is the right trade when the alternative is
// paying for a GPU that idles while it downloads.
//
// Note this is the host's advertised link speed, not its route to HuggingFace —
// a good proxy, not a guarantee.
const minInetDownMbps = 400

type Client struct {
	apiKey     string
	httpClient *http.Client
	verbose    bool
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) SetVerbose(v bool) {
	c.verbose = v
}

type Offer struct {
	ID           int     `json:"id"`
	GPUName      string  `json:"gpu_name"`
	NumGPUs      int     `json:"num_gpus"`
	GPUMemory    float64 `json:"gpu_ram"`
	DPHTotal     float64 `json:"dph_total"` // dollars per hour
	Reliability  float64 `json:"reliability"`
	MachineID    int     `json:"machine_id"`
	Verification string  `json:"verification"`
}

type InstanceInfo struct {
	ID             int        `json:"id"`
	ActualStatus   string     `json:"actual_status"`
	CurState       string     `json:"cur_state"`
	IntendedStatus string     `json:"intended_status"`
	StatusMsg      string     `json:"status_msg"`
	PublicIPAddr   string     `json:"public_ipaddr"`
	SSHHost        string     `json:"ssh_host"`
	SSHPort        int        `json:"ssh_port"`
	DPHTotal       float64    `json:"dph_total"`
	Verification   string     `json:"verification"`
	MachineID      int        `json:"machine_id"`
	Label          string     `json:"label"`
	ImageUUID      string     `json:"image_uuid"`
	Onstart        string     `json:"onstart"`
	ExtraEnv       [][]string `json:"extra_env"`
	// Ports is a nested map: {"22/tcp": [{"HostPort": "12345"}]}
	Ports map[string][]PortMapping `json:"ports"`
}

type PortMapping struct {
	HostPort string `json:"HostPort"`
}

func (i *InstanceInfo) GetSSHPort() int {
	if mappings, ok := i.Ports["22/tcp"]; ok && len(mappings) > 0 {
		var port int
		fmt.Sscanf(mappings[0].HostPort, "%d", &port)
		return port
	}
	return i.SSHPort
}

type CreateInstanceResponse struct {
	Success     bool        `json:"success"`
	NewContract json.Number `json:"new_contract"`
}

type Invoice struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Timestamp   string `json:"timestamp"`
	Amount      string `json:"amount"`
	Quantity    string `json:"quantity"`
	Rate        string `json:"rate"`
}

// ListSSHKeys returns SSH keys associated with the account.
func (c *Client) ListSSHKeys() ([]map[string]any, error) {
	url := fmt.Sprintf("%s/api/v0/ssh/", baseURL)
	var result []map[string]any
	if err := c.doGet(url, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// CreateSSHKey uploads a public SSH key to the vast.ai account.
func (c *Client) CreateSSHKey(pubKey string) error {
	url := fmt.Sprintf("%s/api/v0/ssh/", baseURL)
	body := map[string]any{"ssh_key": pubKey}
	var resp map[string]any
	if err := c.doPost(url, body, &resp); err != nil {
		return fmt.Errorf("upload SSH key: %w", err)
	}
	return nil
}

// VerifyAPIKey checks if the API key is valid by calling the auth endpoint.
func (c *Client) VerifyAPIKey() error {
	url := fmt.Sprintf("%s/api/v0/auth/apikeys/", baseURL)
	var result any
	if err := c.doGet(url, &result); err != nil {
		return fmt.Errorf("invalid API key: %w", err)
	}
	return nil
}

// SearchOffers finds GPU offers matching the VRAM and GPU count requirements.
// minDiskGB is the minimum *host* free disk to require (container disk + image
// + scratch); pass <= 0 to fall back to a safe default.
func (c *Client) SearchOffers(minGPURAM int, numGPUs int, minDiskGB int) ([]Offer, error) {
	// gpu_ram is in MB on vast.ai API. compute_cap >= 800 keeps us on Ampere or
	// newer: llama.cpp's CUDA build runs on much older cards, but the ggml kernels
	// we care about (flash attention with a quantized KV cache, `-fa on` +
	// --cache-type-v, which the whole catalog depends on) want Ampere-class
	// tensor cores to not fall off a performance cliff.
	// Use 90% threshold to catch GPUs that report slightly less (e.g. RTX 3090 reports ~23GB).
	// cuda_vers >= 12.8 filters by the CUDA *toolkit* installed on the host, but that's
	// not enough — what actually matters for forward compatibility is the *driver* cap.
	// cuda_max_good is the maximum CUDA runtime version the host driver can run; if the
	// image's runtime CUDA exceeds this, CUDA init fails with "Error 804: forward
	// compatibility was attempted on non supported HW" because consumer GPUs
	// (RTX 30xx/40xx) don't implement CUDA forward compat — only datacenter cards do.
	// The llama.cpp server-cuda image is built on nvidia/cuda:12.8.1, so 12.8 is
	// exactly the floor: filtering on cuda_max_good >= 12.8 weeds out under-driver
	// hosts before they ever boot a container. Bumping the pinned image to a newer
	// CUDA base means bumping this too. We keep cuda_vers as a belt-and-suspenders check.
	// disk_space >= minDiskGB ensures the host has enough free disk for our container
	// rootfs request plus image layers + scratch (server-cuda unpacks to ~6 GB).
	// Without this, vast.ai can land us on a near-full host and container creation fails
	// with `docker_build() error writing dockerfile`, which is not recoverable.
	minRAMMB := minGPURAM * 1024 * 90 / 100
	if numGPUs <= 0 {
		numGPUs = 1
	}
	if minDiskGB <= 0 {
		minDiskGB = 60
	}
	// verified==true filters out unverified hosts whose vast.ai worker is often broken
	// (those emit `docker_build() error writing dockerfile` at container creation).
	// reliability2 >= 0.95 further weeds out hosts with flaky recent uptime history.
	url := fmt.Sprintf("%s/api/v0/bundles/?q={\"gpu_ram\":{\"gte\":%d},\"num_gpus\":{\"eq\":%d},\"compute_cap\":{\"gte\":800},\"cuda_vers\":{\"gte\":12.8},\"cuda_max_good\":{\"gte\":12.8},\"disk_space\":{\"gte\":%d},\"inet_down\":{\"gte\":%d},\"verified\":{\"eq\":true},\"reliability2\":{\"gte\":0.95},\"rentable\":{\"eq\":true},\"order\":[[\"dph_total\",\"asc\"]],\"type\":\"on-demand\"}", baseURL, minRAMMB, numGPUs, minDiskGB, minInetDownMbps)

	var offers struct {
		Offers []Offer `json:"offers"`
	}
	if err := c.doGet(url, &offers); err != nil {
		return nil, fmt.Errorf("search offers: %w", err)
	}
	return offers.Offers, nil
}

// CreateInstance accepts an offer and creates a new instance with the given
// container disk size (GB).
func (c *Client) CreateInstance(offerID int, image string, envVars map[string]string, onstart string, diskGB int) (*CreateInstanceResponse, error) {
	url := fmt.Sprintf("%s/api/v0/asks/%d/", baseURL, offerID)

	env := make(map[string]string)
	for k, v := range envVars {
		env[k] = v
	}

	if diskGB <= 0 {
		diskGB = 40
	}

	body := map[string]any{
		"client_id": "me",
		"image":     image,
		"runtype":   "ssh",
		"disk":      diskGB,
		"onstart":   onstart,
		"env":       env,
	}

	var resp CreateInstanceResponse
	if err := c.doPut(url, body, &resp); err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	return &resp, nil
}

// GetInstance returns details of a specific instance.
func (c *Client) GetInstance(instanceID int) (*InstanceInfo, error) {
	url := fmt.Sprintf("%s/api/v0/instances/%d/", baseURL, instanceID)

	var wrapper struct {
		Instances InstanceInfo `json:"instances"`
	}
	if err := c.doGet(url, &wrapper); err != nil {
		return nil, fmt.Errorf("get instance %d: %w", instanceID, err)
	}
	return &wrapper.Instances, nil
}

// ListInstances returns all user instances.
func (c *Client) ListInstances() ([]InstanceInfo, error) {
	url := fmt.Sprintf("%s/api/v0/instances/", baseURL)

	var result struct {
		Instances []InstanceInfo `json:"instances"`
	}
	if err := c.doGet(url, &result); err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	return result.Instances, nil
}

// StopInstance stops a running instance. The instance keeps existing (and keeps
// billing container-disk storage); StartInstance is the inverse.
func (c *Client) StopInstance(instanceID int) error {
	url := fmt.Sprintf("%s/api/v0/instances/%d/", baseURL, instanceID)

	body := map[string]any{"state": "stopped"}
	var resp map[string]any
	if err := c.doPut(url, body, &resp); err != nil {
		return fmt.Errorf("stop instance %d: %w", instanceID, err)
	}
	return nil
}

// StartInstance resumes a stopped instance. vast.ai re-runs the onstart script
// on every container start, so the model server comes back on its own — and the
// GGUF is still in the container disk cache, so nothing is re-downloaded.
// The SSH host/port are reassigned on resume and must be re-read afterwards.
func (c *Client) StartInstance(instanceID int) error {
	url := fmt.Sprintf("%s/api/v0/instances/%d/", baseURL, instanceID)

	body := map[string]any{"state": "running"}
	var resp map[string]any
	if err := c.doPut(url, body, &resp); err != nil {
		return fmt.Errorf("start instance %d: %w", instanceID, err)
	}
	return nil
}

// DestroyInstance destroys an instance permanently.
func (c *Client) DestroyInstance(instanceID int) error {
	url := fmt.Sprintf("%s/api/v0/instances/%d/", baseURL, instanceID)

	if err := c.doDelete(url); err != nil {
		return fmt.Errorf("destroy instance %d: %w", instanceID, err)
	}
	return nil
}

// GetInstanceLogs requests logs for an instance. Returns a URL to download them.
func (c *Client) GetInstanceLogs(instanceID int, tail string) (string, error) {
	url := fmt.Sprintf("%s/api/v0/instances/request_logs/%d/", baseURL, instanceID)
	body := map[string]any{}
	if tail != "" {
		body["tail"] = tail
	}
	var result map[string]any
	if err := c.doPut(url, body, &result); err != nil {
		return "", fmt.Errorf("get logs for instance %d: %w", instanceID, err)
	}
	if logURL, ok := result["result_url"].(string); ok {
		return logURL, nil
	}
	data, _ := json.Marshal(result)
	return "", fmt.Errorf("no log URL in response: %s", string(data))
}

// GetInvoices returns billing invoices.
func (c *Client) GetInvoices() ([]Invoice, error) {
	url := fmt.Sprintf("%s/api/v0/invoices", baseURL)

	var invoices []Invoice
	if err := c.doGet(url, &invoices); err != nil {
		return nil, fmt.Errorf("get invoices: %w", err)
	}
	return invoices, nil
}

func (c *Client) doGet(url string, result any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	return c.doRequest(req, result)
}

func (c *Client) doPost(url string, body any, result any) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doRequest(req, result)
}

func (c *Client) doPut(url string, body any, result any) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doRequest(req, result)
}

func (c *Client) doDelete(url string) error {
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	return c.doRequest(req, nil)
}

func (c *Client) doRequest(req *http.Request, result any) error {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	if c.verbose {
		fmt.Fprintf(os.Stderr, "[HTTP] %s %s\n", req.Method, req.URL.String())
		if req.Body != nil {
			bodyBytes, _ := io.ReadAll(req.Body)
			req.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
			fmt.Fprintf(os.Stderr, "[HTTP] Request body: %s\n", string(bodyBytes))
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.verbose {
			fmt.Fprintf(os.Stderr, "[HTTP] Error: %v\n", err)
		}
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if c.verbose {
		fmt.Fprintf(os.Stderr, "[HTTP] Response %d: %s\n", resp.StatusCode, string(data))
	}

	if resp.StatusCode == 429 {
		return fmt.Errorf("rate limited, retry later")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(data))
	}

	if result != nil {
		if err := json.Unmarshal(data, result); err != nil {
			return fmt.Errorf("decode response: %w (body: %s)", err, string(data))
		}
	}
	return nil
}
