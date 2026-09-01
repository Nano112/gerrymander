package main

import "testing"

func TestStatusSummary(t *testing.T) {
	tests := []struct {
		name string
		rep  statusReport
		want string
	}{
		{name: "healthy", rep: statusReport{}, want: "all districts in order."},
		{name: "warning", rep: statusReport{warnings: 1}, want: "no failures; 1 warning(s) need attention."},
		{name: "failure", rep: statusReport{failures: 2, warnings: 1}, want: "2 problem(s), 1 warning(s) found."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rep.summary(); got != tt.want {
				t.Fatalf("summary() = %q, want %q", got, tt.want)
			}
		})
	}
}
