package dnssync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// mock CF API: an in-memory record set behind the real endpoints.
type mockCF struct {
	records map[string]cfRecord // id → record
	nextID  int
}

func (m *mockCF) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, result any) {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
	}
	mux.HandleFunc("GET /zones/{zone}/dns_records", func(w http.ResponseWriter, r *http.Request) {
		var out []cfRecord
		filter := r.URL.Query().Get("comment")
		for id, rec := range m.records {
			if filter != "" && rec.Comment != filter {
				continue
			}
			rec.ID = id
			out = append(out, rec)
		}
		respond(w, out)
	})
	mux.HandleFunc("POST /zones/{zone}/dns_records", func(w http.ResponseWriter, r *http.Request) {
		var rec cfRecord
		json.NewDecoder(r.Body).Decode(&rec)
		m.nextID++
		id := strings.Repeat("r", m.nextID) // deterministic ids
		m.records[id] = rec
		rec.ID = id
		respond(w, rec)
	})
	mux.HandleFunc("PATCH /zones/{zone}/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		var rec cfRecord
		json.NewDecoder(r.Body).Decode(&rec)
		m.records[r.PathValue("id")] = rec
		respond(w, rec)
	})
	mux.HandleFunc("DELETE /zones/{zone}/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		delete(m.records, r.PathValue("id"))
		respond(w, map[string]any{})
	})
	return mux
}

func TestCloudflareReconcile(t *testing.T) {
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "cf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	z, _ := st.EnsureZone(ctx, core.Zone{Name: "example.com"})
	st.CreateAllocation(ctx, core.Allocation{
		ZoneID: z.ID, Label: "shop", FQDN: "shop.example.com", Kind: core.KindTenant,
		Source: core.SourceSeed, State: core.StateActive,
	})

	mock := &mockCF{records: map[string]cfRecord{
		// a hand-made record that must survive every reconcile
		"hand": {Type: "A", Name: "mail.example.com", Content: "1.2.3.4"},
		// a stale managed record whose allocation no longer exists
		"stale": {Type: "CNAME", Name: "old.example.com", Content: "edge.example.com", Comment: ManagedComment},
	}}
	ts := httptest.NewServer(mock.handler(t))
	defer ts.Close()

	cf := &Cloudflare{
		Store: st, Token: "test", BaseURL: ts.URL,
		Zones: []CFZone{{Zone: "example.com", CFZoneID: "z1", Target: "edge.example.com"}},
	}
	if err := cf.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	var created, handSurvives, staleGone bool
	staleGone = true
	for _, rec := range mock.records {
		switch rec.Name {
		case "shop.example.com":
			created = true
			if rec.Type != "CNAME" || rec.Content != "edge.example.com" || rec.Comment != ManagedComment {
				t.Fatalf("bad created record: %+v", rec)
			}
		case "mail.example.com":
			handSurvives = true
		case "old.example.com":
			staleGone = false
		}
	}
	if !created {
		t.Fatal("record for active allocation not created")
	}
	if !handSurvives {
		t.Fatal("hand-made record was deleted — safety contract broken")
	}
	if !staleGone {
		t.Fatal("stale managed record not removed")
	}

	// idempotent second pass: no new records
	n := len(mock.records)
	if err := cf.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(mock.records) != n {
		t.Fatalf("reconcile not idempotent: %d → %d", n, len(mock.records))
	}
}

func TestRecordType(t *testing.T) {
	if recordType("edge.example.com") != "CNAME" || recordType("10.0.0.1") != "A" {
		t.Fatal("record type detection wrong")
	}
}
