package main

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresURLCredentials(t *testing.T) {
	for _, password := range []string{"ordinary", "base64/secret+value=", "p@ss:word?#%$&"} {
		t.Run(password, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig(postgresURL("app@user", password, "postgres", "app_db"))
			if err != nil {
				t.Fatalf("parse database URL: %v", err)
			}
			conn := cfg.ConnConfig
			if conn.User != "app@user" || conn.Password != password || conn.Host != "postgres" || conn.Port != 5432 || conn.Database != "app_db" {
				t.Fatal("database URL did not preserve connection parameters")
			}
		})
	}
}
