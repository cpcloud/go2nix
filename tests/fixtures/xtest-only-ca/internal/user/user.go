// Package user imports core; core's external test imports user.
package user

import "example.com/xtest-only-ca/internal/core"

func Next() int { return core.Value() + 1 }
