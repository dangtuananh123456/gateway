// Package proxy routes and forwards gateway requests to configured upstreams.
package proxy

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/dangtuananh123456/gateway/internal/config"
)

// Route is an immutable snapshot of one configured upstream route.
type Route struct {
	prefix      string
	stripPrefix string
	upstream    url.URL
}

// Prefix returns the inbound path prefix matched by the route.
func (route Route) Prefix() string {
	return route.prefix
}

// StripPrefix returns the path prefix removed before forwarding.
func (route Route) StripPrefix() string {
	return route.stripPrefix
}

// Upstream returns a copy of the route's upstream URL.
func (route Route) Upstream() url.URL {
	return route.upstream
}

// Table is an immutable route table ordered by descending prefix length.
type Table struct {
	routes []Route
}

// NewTable validates, copies, and orders route configuration.
func NewTable(definitions []config.RouteConfig) (*Table, error) {
	if len(definitions) == 0 {
		return nil, errors.New("create route table: routes must not be empty")
	}

	routes := make([]Route, 0, len(definitions))
	prefixes := make(map[string]struct{}, len(definitions))
	for index, definition := range definitions {
		if err := validateDefinition(index, definition, prefixes); err != nil {
			return nil, err
		}

		upstream, err := url.Parse(definition.Upstream)
		if err != nil {
			return nil, fmt.Errorf("create route table: routes[%d].upstream: %w", index, err)
		}

		routes = append(routes, Route{
			prefix:      definition.Prefix,
			stripPrefix: definition.StripPrefix,
			upstream:    *upstream,
		})
		prefixes[definition.Prefix] = struct{}{}
	}

	sort.Slice(routes, func(left int, right int) bool {
		if len(routes[left].prefix) == len(routes[right].prefix) {
			return routes[left].prefix < routes[right].prefix
		}
		return len(routes[left].prefix) > len(routes[right].prefix)
	})

	return &Table{routes: routes}, nil
}

// Match returns a copy of the most specific route matching requestPath.
func (table *Table) Match(requestPath string) (Route, bool) {
	if table == nil {
		return Route{}, false
	}

	for _, route := range table.routes {
		if hasPathPrefix(requestPath, route.prefix) {
			return route, true
		}
	}

	return Route{}, false
}

func validateDefinition(
	index int,
	definition config.RouteConfig,
	prefixes map[string]struct{},
) error {
	field := fmt.Sprintf("create route table: routes[%d]", index)

	if !isCleanAbsolutePath(definition.Prefix) {
		return fmt.Errorf("%s.prefix must be a clean absolute path", field)
	}
	if _, exists := prefixes[definition.Prefix]; exists {
		return fmt.Errorf("%s.prefix %q is duplicated", field, definition.Prefix)
	}

	if definition.StripPrefix != "" {
		if !isCleanAbsolutePath(definition.StripPrefix) {
			return fmt.Errorf("%s.strip_prefix must be empty or a clean absolute path", field)
		}
		if !hasPathPrefix(definition.Prefix, definition.StripPrefix) {
			return fmt.Errorf("%s.strip_prefix must be a prefix of route prefix", field)
		}
	}

	upstream, err := url.Parse(definition.Upstream)
	if err != nil {
		return fmt.Errorf("%s.upstream must be a valid URL: %w", field, err)
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return fmt.Errorf("%s.upstream scheme must be http or https", field)
	}
	if upstream.Host == "" {
		return fmt.Errorf("%s.upstream must contain a host", field)
	}
	if upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" {
		return fmt.Errorf("%s.upstream must not contain credentials, query, or fragment", field)
	}

	return nil
}

func isCleanAbsolutePath(value string) bool {
	return strings.HasPrefix(value, "/") &&
		!strings.ContainsAny(value, "?#") &&
		path.Clean(value) == value
}

func hasPathPrefix(value string, prefix string) bool {
	if prefix == "/" {
		return strings.HasPrefix(value, "/")
	}

	return value == prefix ||
		strings.HasPrefix(value, prefix+"/")
}
