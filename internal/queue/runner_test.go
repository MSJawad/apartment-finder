package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunnerRun(t *testing.T) {
	r := &Runner{Handlers: map[string]Handler{
		"ok":     func(context.Context, Job) error { return nil },
		"fails":  func(context.Context, Job) error { return errors.New("upstream down") },
		"panics": func(context.Context, Job) error { panic("listing has no location") },
	}}

	tests := []struct {
		kind    string
		wantErr string // empty: no error
	}{
		{kind: "ok"},
		{kind: "fails", wantErr: "upstream down"},
		{kind: "panics", wantErr: "handler panicked"},
		{kind: "unknown", wantErr: `no handler for job kind "unknown"`},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			err := r.run(t.Context(), Job{Kind: tt.kind})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("run() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("run() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
