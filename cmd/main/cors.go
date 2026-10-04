package main

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

const defaultCORSOrigins = "https://cellestial.ru,http://localhost:5173"

func parseCORSOrigins(value string) ([]string, error) {
	origins := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
			u.Hostname() == "" || strings.Contains(u.Host, "*") || u.User != nil ||
			u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") {
			return nil, fmt.Errorf("CORS_ALLOWED_ORIGINS: invalid origin %q; expected an HTTP(S) origin without credentials, path, query or fragment", origin)
		}
	}
	return origins, nil
}
