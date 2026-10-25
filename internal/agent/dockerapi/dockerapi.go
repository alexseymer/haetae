// Package dockerapi is a small Docker Engine API client over the Unix socket.
// It deliberately avoids the official SDK to keep the agent under 50 MB RSS.
package dockerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIVersion is pinned: Docker 20.10+; one-shot stats need 1.41.
const APIVersion = "v1.41"

const maxBody = 8 << 20 // 8 MiB

// Client talks to the Docker Engine API over a Unix socket.
type Client struct {
	http *http.Client
	base string
}

// New returns a client for the Docker socket at path (e.g. /var/run/docker.sock).
func New(socketPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true,
	}
	return &Client{
		http: &http.Client{Transport: tr}, // no Timeout: Events is a stream; rely on ctx
		base: "http://docker/" + APIVersion,
	}
}

// Version holds the daemon version response.
type Version struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
}

// Version returns the daemon version (GET /version).
func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	if err := c.getJSON(ctx, "/version", &v); err != nil {
		return Version{}, err
	}
	return v, nil
}

// Port is a published container port.
type Port struct {
	IP          string `json:"IP"`
	PrivatePort uint16 `json:"PrivatePort"`
	PublicPort  uint16 `json:"PublicPort"`
	Type        string `json:"Type"`
}

// ContainerSummary is one entry from GET /containers/json.
type ContainerSummary struct {
	ID      string   `json:"Id"`
	Names   []string `json:"Names"`
	Image   string   `json:"Image"`
	ImageID string   `json:"ImageID"`
	Created int64    `json:"Created"`
	State   string   `json:"State"`
	Status  string   `json:"Status"`
	Labels  Labels   `json:"Labels"`
	Ports   []Port   `json:"Ports"`
}

// Labels decodes only the labels the agent uses. Arbitrary labels can hold
// anything, so they are never decoded, the same as env values.
type Labels struct {
	ComposeProject string `json:"com.docker.compose.project"`
	ComposeService string `json:"com.docker.compose.service"`
	ImageVersion   string `json:"org.opencontainers.image.version"`
}

// ListContainers returns container summaries (GET /containers/json?all=1|0).
func (c *Client) ListContainers(ctx context.Context, all bool) ([]ContainerSummary, error) {
	allQ := "0"
	if all {
		allQ = "1"
	}
	var list []ContainerSummary
	if err := c.getJSON(ctx, "/containers/json?all="+allQ, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// PortBinding is one host-side published port from inspect.
type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// MountPoint is a mount from container inspect (paths only; never content).
type MountPoint struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

// EndpointIP is one network attachment's addresses.
type EndpointIP struct {
	IPAddress         string `json:"IPAddress"`
	GlobalIPv6Address string `json:"GlobalIPv6Address"`
}

// ContainerDetail is the inspect response for a container. It decodes only
// what the agent uses. Config.Env, Config.Cmd/Entrypoint/Args and labels are
// deliberately absent: environment values, command lines and arbitrary
// labels must never be decoded, so they cannot leak later.
type ContainerDetail struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Created      string `json:"Created"`
	RestartCount int    `json:"RestartCount"`
	Image        string `json:"Image"`
	State        struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		OOMKilled  bool   `json:"OOMKilled"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		// Tty decides whether the log stream is multiplexed.
		Tty   bool   `json:"Tty"`
		User  string `json:"User"` // for misconfig root_user; never env
		Image string `json:"Image"`
	} `json:"Config"`
	HostConfig struct {
		Privileged   bool                     `json:"Privileged"`
		CapAdd       []string                 `json:"CapAdd"`
		NetworkMode  string                   `json:"NetworkMode"`
		PidMode      string                   `json:"PidMode"`
		Binds        []string                 `json:"Binds"`
		PortBindings map[string][]PortBinding `json:"PortBindings"`
	} `json:"HostConfig"`
	Mounts          []MountPoint `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]EndpointIP    `json:"Networks"`
		Ports    map[string][]PortBinding `json:"Ports"`
	} `json:"NetworkSettings"`
}

// ImageDetail is the inspect response for an image (RepoDigests, Created).
type ImageDetail struct {
	ID          string   `json:"Id"`
	RepoTags    []string `json:"RepoTags"`
	RepoDigests []string `json:"RepoDigests"`
	Created     string   `json:"Created"`
}

// NetworkSummary is one entry from GET /networks.
type NetworkSummary struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
	IPAM   struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
}

// InspectContainer returns container details (GET /containers/{id}/json).
func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerDetail, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ContainerDetail{}, fmt.Errorf("dockerapi: empty container id")
	}
	var d ContainerDetail
	path := "/containers/" + url.PathEscape(id) + "/json"
	if err := c.getJSON(ctx, path, &d); err != nil {
		return ContainerDetail{}, err
	}
	return d, nil
}

// InspectImage returns image details (GET /images/{name}/json).
// The response type omits Config.Env and other secret-bearing fields.
func (c *Client) InspectImage(ctx context.Context, ref string) (ImageDetail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ImageDetail{}, fmt.Errorf("dockerapi: empty image ref")
	}
	var d ImageDetail
	path := "/images/" + url.PathEscape(ref) + "/json"
	if err := c.getJSON(ctx, path, &d); err != nil {
		return ImageDetail{}, err
	}
	return d, nil
}

// ListNetworks returns networks (GET /networks).
func (c *Client) ListNetworks(ctx context.Context) ([]NetworkSummary, error) {
	var list []NetworkSummary
	if err := c.getJSON(ctx, "/networks", &list); err != nil {
		return nil, err
	}
	return list, nil
}

// InspectNetwork returns one network (GET /networks/{id}).
func (c *Client) InspectNetwork(ctx context.Context, id string) (NetworkSummary, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return NetworkSummary{}, fmt.Errorf("dockerapi: empty network id")
	}
	var n NetworkSummary
	path := "/networks/" + url.PathEscape(id)
	if err := c.getJSON(ctx, path, &n); err != nil {
		return NetworkSummary{}, err
	}
	return n, nil
}

// PullImage pulls ref (repository:tag or repository@digest) into the local
// daemon. It drains the progress stream and returns when the pull finishes.
// It never creates or restarts containers.
func (c *Client) PullImage(ctx context.Context, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("dockerapi: empty image ref")
	}
	// Docker accepts the full ref in fromImage; tag may be empty for digests.
	from, tag := splitPullRef(ref)
	q := url.Values{}
	q.Set("fromImage", from)
	if tag != "" {
		q.Set("tag", tag)
	}
	path := "/images/create?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("dockerapi: pull: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dockerapi: pull: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError("pull", resp)
	}
	// Drain progress JSON lines; bound total read so a stuck daemon cannot
	// grow the agent without limit.
	limited := io.LimitReader(resp.Body, 32<<20)
	dec := json.NewDecoder(limited)
	for {
		var line struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("dockerapi: pull: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("dockerapi: pull: %s", line.Error)
		}
	}
}

// splitPullRef splits "repo:tag" or "repo@digest" for the images/create API.
func splitPullRef(ref string) (from, tag string) {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	// registry:port/name:tag — split on the last colon after the last slash.
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash && colon >= 0 {
		return ref[:colon], ref[colon+1:]
	}
	return ref, ""
}

// Stats is one container stats sample.
type Stats struct {
	Read     time.Time `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IoServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// Stats returns one sample: GET /containers/{id}/stats?stream=false&one-shot=true.
func (c *Client) Stats(ctx context.Context, id string) (Stats, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Stats{}, fmt.Errorf("dockerapi: empty container id")
	}
	var s Stats
	path := "/containers/" + url.PathEscape(id) + "/stats?stream=false&one-shot=true"
	if err := c.getJSON(ctx, path, &s); err != nil {
		return Stats{}, err
	}
	return s, nil
}

// Event is a Docker Engine event.
type Event struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	// Actor.Attributes (which repeat all labels) is deliberately not decoded.
	Actor struct {
		ID string `json:"ID"`
	} `json:"Actor"`
	TimeNano int64 `json:"timeNano"`
}

// Events streams container events (GET /events?filters={"type":["container"]})
// until ctx ends or the stream fails. It calls fn for each event; it returns
// the stream error (nil when ctx is cancelled).
func (c *Client) Events(ctx context.Context, fn func(Event)) error {
	filters := `{"type":["container"]}`
	path := "/events?filters=" + url.QueryEscape(filters)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("dockerapi: events: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("dockerapi: events: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError("events", resp)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("dockerapi: events: %w", err)
		}
		fn(ev)
	}
}

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("dockerapi: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dockerapi: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError(path, resp)
	}

	limited := io.LimitReader(resp.Body, maxBody)
	body, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("dockerapi: read body: %w", err)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("dockerapi: decode: %w", err)
	}
	return nil
}

func (c *Client) statusError(op string, resp *http.Response) error {
	limited := io.LimitReader(resp.Body, maxBody)
	body, _ := io.ReadAll(limited)
	msg := string(body)
	var wrapped struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Message != "" {
		msg = wrapped.Message
	}
	return fmt.Errorf("dockerapi: %s: status %d: %s", op, resp.StatusCode, msg)
}
