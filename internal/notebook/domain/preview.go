package domain

import "strings"

// Preview возвращает не более шести строк и 1024 кодовых точек Unicode без изменений.
func Preview(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	lines, count := 0, 0
	for i, r := range source {
		if count == 1024 {
			return source[:i]
		}
		if r == '\n' {
			lines++
			if lines == 6 {
				return source[:i]
			}
		}
		count++
	}
	return source
}
