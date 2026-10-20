// Package api provides the embedded, hand-written public API contract.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPI []byte
