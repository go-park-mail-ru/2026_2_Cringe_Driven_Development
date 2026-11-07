package migration

import (
	"flag"
	"io"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/config"
)

// ParseOptions keeps environment credentials out of flag help and parse errors.
func ParseOptions(args []string, getenv func(string) string, out io.Writer) (Options, error) {
	flags := flag.NewFlagSet("notebook-migrate", flag.ContinueOnError)
	flags.SetOutput(out)
	database := flags.String("database-url", "", "PostgreSQL URL (default from environment)")
	bucket := flags.String("bucket", "", "legacy notebook bucket (default from environment)")
	verify := flags.Bool("verify", false, "compare all migrated notebooks to S3 without writing")
	if err := flags.Parse(args); err != nil {
		return Options{}, err
	}
	opts := OptionsFromEnv(getenv)
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "database-url":
			opts.DatabaseURL = *database
		case "bucket":
			opts.Bucket = *bucket
		}
	})
	opts.VerifyOnly = *verify
	return opts, nil
}

// OptionsFromEnv accepts the offline settings or the deployed Compose database
// settings. It deliberately does not load API config (JWT, TOTP and Redis).
func OptionsFromEnv(getenv func(string) string) Options {
	opts := Options{DatabaseURL: getenv("NOTEBOOK_MIGRATION_DATABASE_URL"), Bucket: getenv("MIGRATION_S3_NOTEBOOKS_BUCKET")}
	if opts.Bucket == "" {
		opts.Bucket = getenv("S3_NOTEBOOKS_BUCKET")
	}
	if opts.DatabaseURL == "" && getenv("POSTGRES_HOST") != "" {
		opts.DatabaseURL = (config.Config{DBHost: getenv("POSTGRES_HOST"), DBUser: getenv("POSTGRES_USER"), DBPassword: getenv("POSTGRES_PASSWORD"), DBName: getenv("POSTGRES_DB")}).PostgresURL()
	}
	return opts
}
