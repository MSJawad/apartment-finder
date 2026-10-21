package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadMigrations(t *testing.T) {
	tests := []struct {
		name         string
		files        fstest.MapFS
		wantVersions []string
		wantErr      string
	}{
		{
			name: "sorted by number",
			files: fstest.MapFS{
				"migrations/0002_second.sql": {Data: []byte("SELECT 2")},
				"migrations/0001_first.sql":  {Data: []byte("SELECT 1")},
			},
			wantVersions: []string{"0001_first", "0002_second"},
		},
		{
			name:    "bad file name",
			files:   fstest.MapFS{"migrations/1_first.sql": {Data: []byte("SELECT 1")}},
			wantErr: "name must look like",
		},
		{
			name: "two migrations with one number",
			files: fstest.MapFS{
				"migrations/0001_first.sql": {Data: []byte("SELECT 1")},
				"migrations/0001_other.sql": {Data: []byte("SELECT 1")},
			},
			wantErr: "share a number",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadMigrations(tt.files)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("loadMigrations() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadMigrations() error = %v", err)
			}
			var versions []string
			for _, m := range got {
				versions = append(versions, m.version)
			}
			if strings.Join(versions, ",") != strings.Join(tt.wantVersions, ",") {
				t.Errorf("versions = %v, want %v", versions, tt.wantVersions)
			}
		})
	}
}

func TestEmbeddedMigrationsAreValid(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations embedded")
	}
}
