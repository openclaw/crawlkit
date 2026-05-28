package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	ProtocolVersion = "v1"

	ContractPath = "/v1/contract"
	HealthPath   = "/health"

	AuthPublic    = "public"
	AuthReader    = "reader"
	AuthPublisher = "publisher"
)

type Contract struct {
	ProtocolVersion string      `json:"protocol_version"`
	Service         string      `json:"service,omitempty"`
	Routes          []RouteSpec `json:"routes"`
	Apps            []AppSpec   `json:"apps"`
	Auth            []AuthSpec  `json:"auth,omitempty"`
	Notes           []string    `json:"notes,omitempty"`
}

type RouteSpec struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Auth   string `json:"auth"`
}

type AuthSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type AppSpec struct {
	App          string            `json:"app"`
	Queries      []QuerySpec       `json:"queries,omitempty"`
	IngestTables []IngestTableSpec `json:"ingest_tables,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
}

type QuerySpec struct {
	Name string   `json:"name"`
	Args []string `json:"args,omitempty"`
}

type IngestTableSpec struct {
	Name           string   `json:"name"`
	Columns        []string `json:"columns"`
	PrivacyFilters []string `json:"privacy_filters,omitempty"`
}

func BaseContract() Contract {
	return Contract{
		ProtocolVersion: ProtocolVersion,
		Service:         "crawl-remote",
		Routes: []RouteSpec{
			{Method: http.MethodGet, Path: HealthPath, Auth: AuthPublic},
			{Method: http.MethodGet, Path: ContractPath, Auth: AuthPublic},
			{Method: http.MethodPost, Path: "/v1/auth/github/start", Auth: AuthPublic},
			{Method: http.MethodGet, Path: "/v1/auth/github/callback", Auth: AuthPublic},
			{Method: http.MethodPost, Path: "/v1/auth/github/poll", Auth: AuthPublic},
			{Method: http.MethodPost, Path: "/v1/auth/github/token", Auth: AuthPublic},
			{Method: http.MethodGet, Path: "/v1/whoami", Auth: AuthReader},
			{Method: http.MethodGet, Path: "/v1/archives", Auth: AuthReader},
			{Method: http.MethodGet, Path: "/v1/apps/:app/archives/:archive/status", Auth: AuthReader},
			{Method: http.MethodPost, Path: "/v1/apps/:app/archives/:archive/query", Auth: AuthReader},
			{Method: http.MethodPost, Path: "/v1/apps/:app/archives/:archive/batch-read", Auth: AuthReader},
			{Method: http.MethodPost, Path: "/v1/apps/:app/archives/:archive/ingest", Auth: AuthPublisher},
			{Method: http.MethodPut, Path: "/v1/apps/:app/archives/:archive/sqlite", Auth: AuthPublisher},
		},
		Auth: []AuthSpec{
			{Name: AuthPublic, Description: "no bearer token required"},
			{Name: AuthReader, Description: "bearer token with reader or admin role"},
			{Name: AuthPublisher, Description: "bearer token with publisher or admin role"},
		},
		Apps: []AppSpec{},
		Notes: []string{
			"Worker and D1 deployment live outside crawlkit; crawlkit owns the provider-neutral client and contract.",
			"SQLite upload routes may accept raw SQLite objects or gzip chunk bundle parts/manifests.",
			"Routes and JSON fields are additive within a protocol version.",
		},
	}
}

func (c *Client) Contract(ctx context.Context) (Contract, error) {
	var out Contract
	err := c.do(ctx, http.MethodGet, ContractPath, nil, &out, false)
	return out, err
}

func (c Contract) Validate() error {
	if strings.TrimSpace(c.ProtocolVersion) != ProtocolVersion {
		return fmt.Errorf("unsupported remote protocol version %q", c.ProtocolVersion)
	}
	if len(c.Routes) == 0 {
		return errors.New("remote contract has no routes")
	}
	for _, route := range c.Routes {
		if strings.TrimSpace(route.Method) == "" || strings.TrimSpace(route.Path) == "" {
			return fmt.Errorf("remote contract route is incomplete: %#v", route)
		}
		switch strings.TrimSpace(route.Auth) {
		case AuthPublic, AuthReader, AuthPublisher:
		default:
			return fmt.Errorf("remote contract route %s %s has unknown auth %q", route.Method, route.Path, route.Auth)
		}
	}
	for _, app := range c.Apps {
		if strings.TrimSpace(app.App) == "" {
			return errors.New("remote contract app name is empty")
		}
		for _, table := range app.IngestTables {
			if strings.TrimSpace(table.Name) == "" || len(table.Columns) == 0 {
				return fmt.Errorf("remote contract app %q has incomplete ingest table %#v", app.App, table)
			}
		}
	}
	return nil
}
