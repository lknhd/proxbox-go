package proxmox

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lknhd/proxbox-go/internal/config"
	"github.com/lknhd/proxbox-go/internal/models"
)

const snapshotName = "proxbox_pause"

type Client struct {
	baseURL    string
	token      string
	node       string
	storage    string
	template   string
	bridge     string
	httpClient *http.Client
}

func NewClient(cfg config.ProxmoxConfig) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !cfg.VerifySSL},
	}

	return &Client{
		baseURL:  fmt.Sprintf("https://%s:%d/api2/json", cfg.Host, cfg.Port),
		token:    fmt.Sprintf("PVEAPIToken=%s!%s=%s", cfg.User, cfg.TokenName, cfg.TokenValue),
		node:     cfg.Node,
		storage:  cfg.Storage,
		template: cfg.Template,
		bridge:   cfg.Bridge,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

func (c *Client) CreateContainer(vmid int, hostname string, size models.ContainerSize, sshPublicKeys string) error {
	log.Printf("Creating LXC %d (%s, %s)", vmid, hostname, size.Name)

	params := url.Values{
		"vmid":            {fmt.Sprintf("%d", vmid)},
		"hostname":        {hostname},
		"ostemplate":      {c.template},
		"cores":           {fmt.Sprintf("%d", size.Cores)},
		"memory":          {fmt.Sprintf("%d", size.MemoryMB)},
		"swap":            {"512"},
		"storage":         {c.storage},
		"rootfs":          {fmt.Sprintf("%s:%d", c.storage, size.DiskGB)},
		"net0":            {fmt.Sprintf("name=eth0,bridge=%s,ip=dhcp", c.bridge)},
		"start":           {"0"},
		"unprivileged":    {"1"},
		"ssh-public-keys": {sshPublicKeys},
	}

	upid, err := c.post(fmt.Sprintf("/nodes/%s/lxc", c.node), params)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}

	if err := c.waitForTask(upid); err != nil {
		return fmt.Errorf("create container task: %w", err)
	}

	log.Printf("LXC %d created", vmid)
	return nil
}

func (c *Client) StartContainer(vmid int) error {
	log.Printf("Starting LXC %d", vmid)
	upid, err := c.post(fmt.Sprintf("/nodes/%s/lxc/%d/status/start", c.node, vmid), nil)
	if err != nil {
		return err
	}
	if err := c.waitForTask(upid); err != nil {
		return err
	}
	log.Printf("LXC %d started", vmid)
	return nil
}

func (c *Client) StopContainer(vmid int) error {
	log.Printf("Stopping LXC %d", vmid)
	upid, err := c.post(fmt.Sprintf("/nodes/%s/lxc/%d/status/stop", c.node, vmid), nil)
	if err != nil {
		return err
	}
	if err := c.waitForTask(upid); err != nil {
		return err
	}
	log.Printf("LXC %d stopped", vmid)
	return nil
}

func (c *Client) DestroyContainer(vmid int) error {
	log.Printf("Destroying LXC %d", vmid)
	upid, err := c.delete(fmt.Sprintf("/nodes/%s/lxc/%d?purge=1", c.node, vmid))
	if err != nil {
		return err
	}
	if err := c.waitForTask(upid); err != nil {
		return err
	}
	log.Printf("LXC %d destroyed", vmid)
	return nil
}

func (c *Client) CreateSnapshot(vmid int) error {
	log.Printf("Creating snapshot for LXC %d", vmid)
	params := url.Values{
		"snapname":    {snapshotName},
		"description": {"proxbox pause"},
	}
	upid, err := c.post(fmt.Sprintf("/nodes/%s/lxc/%d/snapshot", c.node, vmid), params)
	if err != nil {
		return err
	}
	if err := c.waitForTask(upid); err != nil {
		return err
	}
	log.Printf("Snapshot created for LXC %d", vmid)
	return nil
}

func (c *Client) RollbackSnapshot(vmid int) error {
	log.Printf("Rolling back snapshot for LXC %d", vmid)
	upid, err := c.post(fmt.Sprintf("/nodes/%s/lxc/%d/snapshot/%s/rollback", c.node, vmid, snapshotName), nil)
	if err != nil {
		return err
	}
	if err := c.waitForTask(upid); err != nil {
		return err
	}
	log.Printf("Snapshot rolled back for LXC %d", vmid)
	return nil
}

func (c *Client) DeleteSnapshot(vmid int) error {
	log.Printf("Deleting snapshot for LXC %d", vmid)
	upid, err := c.delete(fmt.Sprintf("/nodes/%s/lxc/%d/snapshot/%s", c.node, vmid, snapshotName))
	if err != nil {
		return err
	}
	if err := c.waitForTask(upid); err != nil {
		log.Printf("Warning: failed to delete snapshot for LXC %d: %v", vmid, err)
		return nil
	}
	log.Printf("Snapshot deleted for LXC %d", vmid)
	return nil
}

func (c *Client) GetContainerIP(vmid int) string {
	body, err := c.get(fmt.Sprintf("/nodes/%s/lxc/%d/interfaces", c.node, vmid))
	if err != nil {
		return ""
	}

	var resp struct {
		Data []struct {
			Name string `json:"name"`
			Inet string `json:"inet"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}

	for _, iface := range resp.Data {
		if iface.Name == "eth0" || iface.Name == "veth0" {
			if iface.Inet != "" && strings.Contains(iface.Inet, "/") {
				ip := strings.Split(iface.Inet, "/")[0]
				if !strings.HasPrefix(ip, "127.") {
					return ip
				}
			}
		}
	}
	return ""
}

func (c *Client) WaitForIP(vmid int, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	delay := 1 * time.Second
	for time.Now().Before(deadline) {
		ip := c.GetContainerIP(vmid)
		if ip != "" && !strings.HasPrefix(ip, "fe80") {
			return ip
		}
		time.Sleep(delay)
		if delay < 4*time.Second {
			delay = time.Duration(float64(delay) * 1.5)
		}
	}
	return ""
}

// HTTP helpers

func (c *Client) get(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) post(path string, params url.Values) (string, error) {
	var body io.Reader
	if params != nil {
		body = strings.NewReader(params.Encode())
	}

	req, err := http.NewRequest("POST", c.baseURL+path, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.token)
	if params != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil
	}
	return result.Data, nil
}

func (c *Client) delete(path string) (string, error) {
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", nil
	}
	return result.Data, nil
}

func (c *Client) waitForTask(upid string) error {
	if upid == "" {
		return nil
	}

	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		body, err := c.get(fmt.Sprintf("/nodes/%s/tasks/%s/status", c.node, url.PathEscape(upid)))
		if err != nil {
			return err
		}

		var resp struct {
			Data struct {
				Status     string `json:"status"`
				ExitStatus string `json:"exitstatus"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return err
		}

		if resp.Data.Status == "stopped" {
			if resp.Data.ExitStatus != "OK" {
				return fmt.Errorf("task failed: %s", resp.Data.ExitStatus)
			}
			return nil
		}

		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("task timed out: %s", upid)
}
