// Package api serves strata's HTTP API. The OpenAPI spec in api/openapi.yaml
// is the source of truth: types and the strict server interface are
// generated from it, and handlers here satisfy that interface by calling
// the service.
package api

//go:generate go tool oapi-codegen -config ../../api/types.cfg.yaml ../../api/openapi.yaml
//go:generate go tool oapi-codegen -config ../../api/server.cfg.yaml ../../api/openapi.yaml
