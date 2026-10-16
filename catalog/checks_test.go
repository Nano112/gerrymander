package catalog

import "testing"

func TestHTTPCheckDefinitions(t *testing.T) {
	c := HTTPCheck{ID: "app", Name: "App", URL: "https://example.com/up", Statuses: []int{200}, JSON: map[string]any{"ok": true}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []HTTPCheck{
		{ID: "app", Name: "App", URL: "https://user:secret@example.com/up"},
		{ID: "app", Name: "App", URL: "file:///secret"},
		{ID: "app", Name: "App", URL: c.URL, Statuses: []int{999}},
		{ID: "app", Name: "App", URL: c.URL, JSON: map[string]any{"nested": map[string]string{"token": "secret"}}},
		{ID: "app", Name: "App", URL: c.URL, Redirect: &RedirectExpectation{Origin: "https://example.com/path", PathPrefix: "/"}},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted invalid definition: %+v", bad)
		}
	}
}
