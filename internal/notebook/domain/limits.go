package domain

import "errors"

// ErrTooLarge означает превышение лимита данных блокнота.
var ErrTooLarge = errors.New("notebook size limit exceeded")
