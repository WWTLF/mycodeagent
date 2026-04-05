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
	ID         int     `json:"id"`
	GPUName    string  `json:"gpu_name"`
	NumGPUs    int     `json:"num_gpus"`
	GPUMemory  float64 `json:"gpu_ram"`
	DPHTotal   float64 `json:"dph_total"` // dollars per hour
	Reliability float64 `json:"reliability"`
}

type InstanceInfo struct {
	ID           int     `json:"id"`
	ActualStatus string  `json:"actual_status"`
	PublicIPAddr string  `json:"public_ipaddr"`
	SSHPort      int     `json:"ssh_port"`
	DPHTotal     float64 `json:"dph_total"`
	Label        string  `json:"label"`
	ImageUUID    string  `json:"image_uuid"`
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
	Success     bool   `json:"success"`
	NewContract string `json:"new_contract"`
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
func (c *Client) SearchOffers(minGPURAM int, numGPUs int) ([]Offer, error) {
	// gpu_ram is in MB on vast.ai API; compute_cap >= 800 required for AWQ quantization
	// Use 90% threshold to catch GPUs that report slightly less (e.g. RTX 3090 reports ~23GB)
	minRAMMB := minGPURAM * 1024 * 90 / 100
	if numGPUs <= 0 {
		numGPUs = 1
	}
	url := fmt.Sprintf("%s/api/v0/bundles/?q={\"gpu_ram\":{\"gte\":%d},\"num_gpus\":{\"eq\":%d},\"compute_cap\":{\"gte\":800},\"order\":[[\"dph_total\",\"asc\"]],\"type\":\"on-demand\"}", baseURL, minRAMMB, numGPUs)

	var offers struct {
		Offers []Offer `json:"offers"`
	}
	if err := c.doGet(url, &offers); err != nil {
		return nil, fmt.Errorf("search offers: %w", err)
	}
	return offers.Offers, nil
}

// CreateInstance accepts an offer and creates a new instance.
func (c *Client) CreateInstance(offerID int, image string, envVars map[string]string, onstart string) (*CreateInstanceResponse, error) {
	url := fmt.Sprintf("%s/api/v0/asks/%d/", baseURL, offerID)

	env := make(map[string]string)
	for k, v := range envVars {
		env[k] = v
	}

	body := map[string]any{
		"client_id": "me",
		"image":     image,
		"runtype":   "ssh",
		"disk":      40,
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

	var inst InstanceInfo
	if err := c.doGet(url, &inst); err != nil {
		return nil, fmt.Errorf("get instance %d: %w", instanceID, err)
	}
	return &inst, nil
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

// StopInstance stops a running instance.
func (c *Client) StopInstance(instanceID int) error {
	url := fmt.Sprintf("%s/api/v0/instances/%d/", baseURL, instanceID)

	body := map[string]any{"target_state": "stopped"}
	var resp map[string]any
	if err := c.doPut(url, body, &resp); err != nil {
		return fmt.Errorf("stop instance %d: %w", instanceID, err)
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
