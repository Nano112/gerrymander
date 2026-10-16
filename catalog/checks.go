package catalog

import (
	"fmt"
	"net/url"
	"strings"
)

// HTTPCheck declares a probe for a monitoring consumer. Gerry stores the
// definition but never performs probes or interprets registration as health.
// Credentials and authorization headers deliberately have no representation.
type HTTPCheck struct {
	ID          string               `json:"id" yaml:"id"`
	Name        string               `json:"name" yaml:"name"`
	URL         string               `json:"url" yaml:"url"`
	Description string               `json:"description,omitempty" yaml:"description,omitempty"`
	Statuses    []int                `json:"statuses,omitempty" yaml:"statuses,omitempty"`
	Contains    string               `json:"contains,omitempty" yaml:"contains,omitempty"`
	JSON        map[string]any       `json:"json,omitempty" yaml:"json,omitempty"`
	Redirect    *RedirectExpectation `json:"redirect,omitempty" yaml:"redirect,omitempty"`
}

type RedirectExpectation struct {
	Origin     string `json:"origin" yaml:"origin"`
	PathPrefix string `json:"pathPrefix" yaml:"path_prefix"`
}

func (c HTTPCheck) Validate() error {
	if !ValidID(c.ID) || strings.TrimSpace(c.Name) == "" || len(c.Name) > 160 || !WebURL(c.URL) || len(c.Description) > 4096 || len(c.Contains) > 4096 || len(c.Statuses) > 20 || len(c.JSON) > 30 {
		return fmt.Errorf("invalid HTTP check %q", c.ID)
	}
	for _, status := range c.Statuses {
		if status < 100 || status > 599 {
			return fmt.Errorf("invalid HTTP check status")
		}
	}
	for k, v := range c.JSON {
		if k == "" || len(k) > 100 {
			return fmt.Errorf("invalid HTTP check JSON key")
		}
		switch v := v.(type) {
		case string:
			if len(v) > 4096 {
				return fmt.Errorf("HTTP check JSON value too long")
			}
		case bool, float64, int, int64, nil:
		default:
			return fmt.Errorf("HTTP check JSON expectations must be scalars")
		}
	}
	if c.Redirect != nil {
		u, err := url.Parse(c.Redirect.Origin)
		if err != nil || !WebURL(c.Redirect.Origin) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(c.Redirect.PathPrefix, "/") || len(c.Redirect.PathPrefix) > 1024 {
			return fmt.Errorf("invalid redirect expectation")
		}
	}
	return nil
}
