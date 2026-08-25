// Package api embeds first-wave OpenAPI contracts for runtime discovery.
package api

import _ "embed"

// OpenAPIV1 is the current app-server OpenAPI 3 document.
//
//go:embed openapi-v1.yaml
var OpenAPIV1 []byte
