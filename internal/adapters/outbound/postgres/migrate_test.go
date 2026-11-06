package postgres

import "testing"

// TestMigrationsURL pins the PgBouncer rule: DDL runs against
// MIGRATIONS_DATABASE_URL when it is set and non-empty, and falls back to
// DATABASE_URL (which may point at a pooler) otherwise.
func TestMigrationsURL(t *testing.T) {
	cases := []struct {
		name   string
		getenv func(string) string
		want   string
	}{
		{
			name: "migrations url wins",
			getenv: func(k string) string {
				return map[string]string{
					EnvMigrationsDatabaseURL: "postgres://direct:5432/dock_scheduling",
					EnvDatabaseURL:           "postgres://pgbouncer:6432/dock_scheduling",
				}[k]
			},
			want: "postgres://direct:5432/dock_scheduling",
		},
		{
			name: "empty migrations url falls back to database url",
			getenv: func(k string) string {
				return map[string]string{
					EnvMigrationsDatabaseURL: "",
					EnvDatabaseURL:           "postgres://pgbouncer:6432/dock_scheduling",
				}[k]
			},
			want: "postgres://pgbouncer:6432/dock_scheduling",
		},
		{
			name:   "unset migrations url falls back to database url",
			getenv: func(k string) string { return "postgres://only:5432/dock_scheduling" },
			want:   "postgres://only:5432/dock_scheduling",
		},
		{
			name:   "nothing set is empty",
			getenv: func(string) string { return "" },
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MigrationsURL(tc.getenv); got != tc.want {
				t.Fatalf("MigrationsURL = %q, want %q", got, tc.want)
			}
		})
	}
}
