package migration

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestParseOptionsDoesNotExposeCredentials(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"-unknown"}} {
		var out bytes.Buffer
		_, err := ParseOptions(args, func(key string) string {
			return map[string]string{"POSTGRES_HOST": "postgres", "POSTGRES_PASSWORD": "SECRET-password", "NOTEBOOK_MIGRATION_DATABASE_URL": "postgres://user:SECRET-explicit@host/db"}[key]
		}, &out)
		if err == nil || strings.Contains(out.String(), "SECRET") {
			t.Fatal("expected safe help or argument error")
		}
		if args[0] == "-h" && !errors.Is(err, flag.ErrHelp) {
			t.Fatal("help must be distinguishable from failure")
		}
	}
}

func TestParseOptionsFlagsOverrideEnvironment(t *testing.T) {
	for _, database := range []string{"postgres://flag/db", ""} {
		var out bytes.Buffer
		got, err := ParseOptions([]string{"-database-url=" + database, "-bucket=flag-bucket", "-verify"}, func(key string) string {
			return map[string]string{"NOTEBOOK_MIGRATION_DATABASE_URL": "postgres://env/db", "MIGRATION_S3_NOTEBOOKS_BUCKET": "env-bucket"}[key]
		}, &out)
		if err != nil || got != (Options{DatabaseURL: database, Bucket: "flag-bucket", VerifyOnly: true}) {
			t.Fatal("explicit flags must override environment, including empty values")
		}
	}
}

func TestParseOptionsUsesEnvironmentWithoutFlags(t *testing.T) {
	var out bytes.Buffer
	got, err := ParseOptions(nil, func(key string) string {
		return map[string]string{"POSTGRES_HOST": "postgres", "POSTGRES_USER": "user", "POSTGRES_PASSWORD": "pass", "POSTGRES_DB": "db", "S3_NOTEBOOKS_BUCKET": "legacy"}[key]
	}, &out)
	if err != nil || got != (Options{DatabaseURL: "postgres://user:pass@postgres:5432/db", Bucket: "legacy"}) {
		t.Fatal("flags must fall back to offline environment settings")
	}
}

func TestOptionsFromComposeEnvironment(t *testing.T) {
	values := map[string]string{"POSTGRES_HOST": "postgres", "POSTGRES_USER": "user@name", "POSTGRES_PASSWORD": "p:/?#$", "POSTGRES_DB": "app_db", "S3_NOTEBOOKS_BUCKET": "legacy"}
	opts := OptionsFromEnv(func(k string) string { return values[k] })
	cfg, err := pgxpool.ParseConfig(opts.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.ConnConfig
	if c.Host != "postgres" || c.Port != 5432 || c.User != "user@name" || c.Password != "p:/?#$" || c.Database != "app_db" || opts.Bucket != "legacy" {
		t.Fatalf("incorrect migration settings (credentials omitted)")
	}
	values["NOTEBOOK_MIGRATION_DATABASE_URL"] = "postgres://explicit/db"
	values["MIGRATION_S3_NOTEBOOKS_BUCKET"] = "explicit"
	opts = OptionsFromEnv(func(k string) string { return values[k] })
	if opts.DatabaseURL != values["NOTEBOOK_MIGRATION_DATABASE_URL"] || opts.Bucket != "explicit" {
		t.Fatal("explicit migration settings must take precedence")
	}
}

func TestOptionsFromEnvPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]string
		want   Options
	}{
		{name: "missing settings", values: map[string]string{}, want: Options{}},
		{name: "legacy bucket", values: map[string]string{"S3_NOTEBOOKS_BUCKET": "old"}, want: Options{Bucket: "old"}},
		{name: "migration bucket", values: map[string]string{"MIGRATION_S3_NOTEBOOKS_BUCKET": "new", "S3_NOTEBOOKS_BUCKET": "old"}, want: Options{Bucket: "new"}},
		{name: "explicit database without API config", values: map[string]string{"NOTEBOOK_MIGRATION_DATABASE_URL": "postgres://explicit/db?sslmode=disable"}, want: Options{DatabaseURL: "postgres://explicit/db?sslmode=disable"}},
		{name: "explicit database overrides Compose", values: map[string]string{"NOTEBOOK_MIGRATION_DATABASE_URL": "postgres://explicit/db", "POSTGRES_HOST": "ignored", "POSTGRES_USER": "ignored", "POSTGRES_PASSWORD": "ignored", "POSTGRES_DB": "ignored"}, want: Options{DatabaseURL: "postgres://explicit/db"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := OptionsFromEnv(func(key string) string { return tt.values[key] })
			if got != tt.want {
				t.Fatal("incorrect offline settings (credentials omitted)")
			}
		})
	}
}
