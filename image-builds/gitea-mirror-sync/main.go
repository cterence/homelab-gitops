// gitea-mirror-sync reconciles a declarative list of git mirrors against a
// running Gitea instance. It creates missing mirrors, updates settings on
// existing ones, and prunes mirrors that are no longer in the config.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the YAML document mounted into the reconciler.
type Config struct {
	Mirrors []Mirror `yaml:"mirrors"`
}

// Mirror is a single desired mirror entry.
type Mirror struct {
	Owner          string `yaml:"owner"          json:"owner"`
	Name           string `yaml:"name"           json:"name"`
	CloneAddr      string `yaml:"clone_addr"     json:"clone_addr"`
	MirrorInterval string `yaml:"mirror_interval" json:"mirror_interval,omitempty"`
	Private        bool   `yaml:"private"        json:"private"`
	Wiki           bool   `yaml:"wiki"           json:"wiki"`
}

// repo is the subset of Gitea's repository response that we use.
type repo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Mirror   bool   `json:"mirror"`
	Private  bool   `json:"private"`
	Owner    struct {
		Name string `json:"login"`
	} `json:"owner"`
}

type client struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client
}

func newClient(baseURL, user, pass string) *client {
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		user:    user,
		pass:    pass,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

// getRepo returns the repo if it exists, nil on 404.
func (c *client) getRepo(ctx context.Context, owner, name string) (*repo, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/repos/%s/%s", owner, name), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil
	case resp.StatusCode == http.StatusOK:
		var r repo
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, fmt.Errorf("decode repo: %w", err)
		}
		return &r, nil
	default:
		return nil, fmt.Errorf("GET repo: %s", bodyText(resp))
	}
}

// migrate creates a new mirror via /repos/migrate.
func (c *client) migrate(ctx context.Context, m Mirror) error {
	payload := map[string]any{
		"clone_addr": m.CloneAddr,
		"repo_owner": m.Owner,
		"repo_name":  m.Name,
		"mirror":     true,
		"private":    m.Private,
		"wiki":       m.Wiki,
	}
	if m.MirrorInterval != "" {
		payload["mirror_interval"] = m.MirrorInterval
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/v1/repos/migrate", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("migrate %s/%s: %s", m.Owner, m.Name, bodyText(resp))
	}
	return nil
}

// updateRepo patches mirror_interval / private on an existing mirror.
func (c *client) updateRepo(ctx context.Context, r *repo, m Mirror) error {
	payload := map[string]any{
		"private": m.Private,
	}
	if m.MirrorInterval != "" {
		payload["mirror_interval"] = m.MirrorInterval
	}
	resp, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/api/v1/repos/%s/%s", m.Owner, m.Name), payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("patch %s/%s: %s", m.Owner, m.Name, bodyText(resp))
	}
	return nil
}

// listRepos returns all repos owned by owner that the token can see.
func (c *client) listRepos(ctx context.Context, owner string) ([]repo, error) {
	var out []repo
	page := 1
	for {
		path := fmt.Sprintf("/api/v1/repos/search?owner=%s&limit=50&page=%d", url.QueryEscape(owner), page)
		resp, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return nil, fmt.Errorf("search repos for %s: %s", owner, resp.Status)
		}
		var pageResp struct {
			Data []repo `json:"data"`
			Ok   bool   `json:"ok"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&pageResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode search: %w", err)
		}
		resp.Body.Close()
		out = append(out, pageResp.Data...)
		if len(pageResp.Data) < 50 {
			break
		}
		page++
	}
	// /repos/search returns matches across all owners when owner filter is
	// applied loosely; filter client-side to be safe.
	var filtered []repo
	for _, r := range out {
		if r.Owner.Name == owner {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

func (c *client) deleteRepo(ctx context.Context, owner, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/repos/%s/%s", owner, name), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete %s/%s: %s", owner, name, bodyText(resp))
	}
	return nil
}

func bodyText(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	return resp.Status + ": " + string(b)
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	seen := map[string]bool{}
	for i := range cfg.Mirrors {
		m := &cfg.Mirrors[i]
		if m.Owner == "" || m.Name == "" || m.CloneAddr == "" {
			return nil, fmt.Errorf("entry %d: owner, name and clone_addr are required", i)
		}
		key := m.Owner + "/" + m.Name
		if seen[key] {
			return nil, fmt.Errorf("duplicate mirror %s", key)
		}
		seen[key] = true
	}
	return &cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gitea-mirror-sync: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	mirrorsFile := env("MIRRORS_FILE", "/etc/mirrors/mirrors.yaml")
	dryRun := os.Getenv("DRY_RUN") == "true"
	prune := os.Getenv("PRUNE")
	if prune == "" {
		prune = "true"
	}
	shouldPrune := prune == "true"

	cfg, err := loadConfig(mirrorsFile)
	if err != nil {
		return err
	}
	fmt.Printf("loaded %d mirror(s) from %s\n", len(cfg.Mirrors), mirrorsFile)

	c := newClient(env("GITEA_URL", ""), os.Getenv("GITEA_USER"), os.Getenv("GITEA_PASS"))

	// Reconcile desired state.
	desired := map[string]bool{}
	for _, m := range cfg.Mirrors {
		key := m.Owner + "/" + m.Name
		desired[key] = true

		r, err := c.getRepo(ctx, m.Owner, m.Name)
		if err != nil {
			return fmt.Errorf("checking %s: %w", key, err)
		}
		switch {
		case r == nil:
			fmt.Printf("create mirror %s <- %s\n", key, m.CloneAddr)
			if !dryRun {
				if err := c.migrate(ctx, m); err != nil {
					return err
				}
			}
		case !r.Mirror:
			fmt.Printf("error: %s exists but is not a mirror; refusing to convert it\n", key)
			continue
		default:
			if m.MirrorInterval != "" || r.Private != m.Private {
				fmt.Printf("update mirror %s (mirror_interval=%s private=%v)\n", key, m.MirrorInterval, m.Private)
				if !dryRun {
					if err := c.updateRepo(ctx, r, m); err != nil {
						return err
					}
				}
			} else {
				fmt.Printf("ok mirror %s\n", key)
			}
		}
	}

	// Prune mirrors in any managed owner that are no longer desired.
	if !shouldPrune {
		fmt.Println("prune disabled; skipping cleanup")
		return nil
	}
	owners := map[string]bool{}
	for _, m := range cfg.Mirrors {
		owners[m.Owner] = true
	}
	for owner := range owners {
		repos, err := c.listRepos(ctx, owner)
		if err != nil {
			return fmt.Errorf("listing repos for %s: %w", owner, err)
		}
		for _, r := range repos {
			key := r.Owner.Name + "/" + r.Name
			if !r.Mirror {
				continue // never touch non-mirror repos
			}
			if desired[key] {
				continue
			}
			fmt.Printf("prune mirror %s\n", key)
			if !dryRun {
				if err := c.deleteRepo(ctx, r.Owner.Name, r.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
