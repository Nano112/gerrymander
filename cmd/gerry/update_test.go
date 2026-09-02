package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFileCapabilities(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "no capability", in: "/usr/local/bin/gerry\n", want: ""},
		{name: "bind service", in: "/usr/local/bin/gerry cap_net_bind_service=ep\n", want: lowPortCapability},
		{name: "numeric owner", in: "/usr/local/bin/gerry cap_net_bind_service=ep [rootid=0]\n", want: lowPortCapability},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseFileCapabilities(tt.in); got != tt.want {
				t.Fatalf("parseFileCapabilities(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHasLowPortCapability(t *testing.T) {
	if !hasLowPortCapability("cap_net_bind_service,cap_net_raw=ep") {
		t.Fatal("combined capability set should include low-port binding")
	}
	if hasLowPortCapability("cap_net_raw=ep") {
		t.Fatal("unrelated capability reported as low-port binding")
	}
}

func TestReplaceExecutableDirect(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "downloaded")
	dst := filepath.Join(dir, "gerry")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceExecutable(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("replacement contents = %q, want new", got)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("replacement mode = %o, want 755", st.Mode().Perm())
	}
}
