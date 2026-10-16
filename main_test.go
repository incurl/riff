// Tests for the riff binary entrypoint. The actual migration logic
// lives under pb/migrations; the import below is for its side effect
// (each migration registers itself via init()).
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/dbutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yaml "go.yaml.in/yaml/v3"

	_ "github.com/incurl/riff/pb/migrations"

	"github.com/incurl/riff/server/mcp"
)

// TestExtendUsersMigration_FieldsAndRules boots a fresh PocketBase app
// in a temporary data directory, applies all system + user migrations,
// and asserts that the `users` auth collection has been extended with
// the PO-33 profile fields and the access rules we specified.
func TestExtendUsersMigration_FieldsAndRules(t *testing.T) {
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err, "users collection should exist after migrations")
	require.Equal(t, core.CollectionTypeAuth, users.Type, "users must be an auth collection")

	// Field presence checks.
	username := users.Fields.GetByName("username")
	require.NotNil(t, username, "username field must be added")
	assert.Equal(t, core.FieldTypeText, username.Type())

	bio := users.Fields.GetByName("bio")
	require.NotNil(t, bio, "bio field must be added")
	assert.Equal(t, core.FieldTypeText, bio.Type())

	avatar := users.Fields.GetByName("avatar")
	require.NotNil(t, avatar, "built-in avatar file field must still be present")
	assert.Equal(t, core.FieldTypeFile, avatar.Type())

	// Unique index on username. PocketBase's Indexes field is a
	// JSONArray[string] of raw CREATE INDEX SQL statements; we use
	// the helper methods to look up by name and then parse the
	// expression to confirm uniqueness.
	idxSQL := users.GetIndex("idx_users_username")
	require.NotEmpty(t, idxSQL, "idx_users_username index must be present")

	parsed := dbutils.ParseIndex(idxSQL)
	assert.True(t, parsed.Unique, "idx_users_username must be unique")

	// Access rules: list/view public, update/delete self-only.
	require.NotNil(t, users.ListRule, "ListRule must be set")
	assert.Equal(t, "", *users.ListRule, "ListRule must be public")

	require.NotNil(t, users.ViewRule, "ViewRule must be set")
	assert.Equal(t, "", *users.ViewRule, "ViewRule must be public")

	require.NotNil(t, users.UpdateRule, "UpdateRule must be set")
	assert.Equal(t, "id = @request.auth.id", *users.UpdateRule, "UpdateRule must be self-only")

	require.NotNil(t, users.DeleteRule, "DeleteRule must be set")
	assert.Equal(t, "id = @request.auth.id", *users.DeleteRule, "DeleteRule must be self-only")
}

// TestExtendUsersMigration_CreateAndReadBack creates an auth record
// with the custom PO-33 fields populated and reads it back.
func TestExtendUsersMigration_CreateAndReadBack(t *testing.T) {
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)

	rec := core.NewRecord(users)
	rec.Set("username", "ada")
	rec.Set("email", "ada@example.com")
	rec.Set("password", "correct horse battery staple")
	rec.Set("bio", "Pocket-Operator enthusiast. Currently sampling rain.")

	require.NoError(t, app.Save(rec), "save should succeed")

	got, err := app.FindRecordById("users", rec.Id)
	require.NoError(t, err)
	assert.Equal(t, "ada", got.GetString("username"))
	assert.Equal(t, "ada@example.com", got.GetString("email"))
	assert.Equal(t, "Pocket-Operator enthusiast. Currently sampling rain.", got.GetString("bio"))
}

// TestRemoveIsCreatorMigration_FieldGone pins the removal of the
// `is_creator` BoolField. The field was added in
// 1700000001_extend_users.go and dropped in
// 1700000002_remove_is_creator.go. After both migrations run, the
// `users` collection must not contain the field — this test fails
// loudly if a future change re-introduces it (e.g. by mistake in a
// rebase of the original migration) so the schema shape stays
// pinned to a single source of truth.
func TestRemoveIsCreatorMigration_FieldGone(t *testing.T) {
	app := newTestApp(t)

	users, err := app.FindCollectionByNameOrId("users")
	require.NoError(t, err)
	require.Nil(t, users.Fields.GetByName("is_creator"),
		"is_creator must be removed by 1700000002_remove_is_creator.go; "+
			"if you re-added it intentionally, update this test and "+
			"audit every wire surface for the field first")
}

// newTestApp builds a fresh PocketBase app in a per-test temp dir,
// bootstraps it, and runs all migrations. Cleanup is automatic via
// t.TempDir() and t.Cleanup.
func newTestApp(t *testing.T) core.App {
	t.Helper()

	app := core.NewBaseApp(core.BaseAppConfig{
		DataDir:       t.TempDir(),
		EncryptionEnv: "pb_test_env_riff",
	})
	t.Cleanup(func() {
		_ = app.ClearBootstrap()
	})

	require.NoError(t, app.Bootstrap(), "Bootstrap should succeed")
	_, err := app.DB().NewQuery("SELECT 1").Execute()
	require.NoError(t, err, "DB should be reachable after Bootstrap")
	_, err = app.AuxDB().NewQuery("SELECT 1").Execute()
	require.NoError(t, err, "AuxDB should be reachable after Bootstrap")

	require.NoError(t, app.RunAllMigrations(), "RunAllMigrations should succeed")
	return app
}

// TestMCP_RegisterMissingSpecFailsFast checks that mcp.Register
// surfaces a clear error when the OpenAPI spec file does not
// exist. The failure has to be the one main.go would have
// surfaced to operators; a "spec required" or "spec not found"
// error is the contract so a missing buf generate step is
// immediately diagnosed. The check runs against the OnServe hook
// directly because the construction of the service is deferred
// there.
func TestMCP_RegisterMissingSpecFailsFast(t *testing.T) {
	app := newTestApp(t)

	// Register attaches an OnServe hook. mcp.Register itself
	// returns nil; the failure must come from the OnServe
	// binding, exactly as it would during app.Start() in main.go.
	// The profile is nil because this test is about the spec
	// file, not origin matching.
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	require.NoError(t, mcp.Register(app, nil, missingPath))

	baseRouter, err := apis.NewRouter(app)
	require.NoError(t, err)
	serveEvent := new(core.ServeEvent)
	serveEvent.App = app
	serveEvent.Router = baseRouter

	triggerErr := app.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error { return e.Next() })
	require.Error(t, triggerErr, "missing spec must surface as an OnServe error")
	// The error chain must include the path the operator passed
	// in. The exact wording ("openapi.yaml" / "buf generate") is
	// only used for the empty-path case, but a missing file
	// still has to carry the path the operator can grep for.
	assert.Contains(t, triggerErr.Error(), missingPath,
		"error must mention the spec path the operator passed in")
}

// TestMCP_RegisterMountsEndpoint checks the happy path: with a
// valid OpenAPI spec, mcp.Register binds /mcp on the same router
// the rest of the app uses, and a request to that path gets a
// real MCP response instead of a 404.
//
// The MCP service refuses to start unless the spec contains
// every operation in the curated list, so the test generates a
// minimal spec on the fly from mcp.CuratedOperationIDs() and
// writes it to a temp file. Real-world specs add a lot more
// detail (path-parameter substitution, request-body schemas,
// etc.) — those are exercised by the adapter tests under
// server/mcp, not by this wiring test.
func TestMCP_RegisterMountsEndpoint(t *testing.T) {
	specPath := writeMinimalOpenAPISpec(t)

	app, err := tests.NewTestApp()
	require.NoError(t, err)
	t.Cleanup(app.Cleanup)

	// nil profile: this test is about /mcp wiring, not origin matching.
	require.NoError(t, mcp.Register(app, nil, specPath))

	baseRouter, err := apis.NewRouter(app)
	require.NoError(t, err)
	serveEvent := new(core.ServeEvent)
	serveEvent.App = app
	serveEvent.Router = baseRouter
	var mux http.Handler
	require.NoError(t, app.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error {
		m, err := e.Router.BuildMux()
		if err != nil {
			return err
		}
		mux = m
		return nil
	}))

	// Initialize handshake to verify the service speaks the
	// protocol. The test deliberately uses the legacy 2025-06-18
	// protocol version (which the SDK falls back to by default
	// when MCP-Protocol-Version is absent): it only needs a
	// successful round-trip to prove the endpoint is wired up,
	// and the new 2026-07-28 protocol carries additional
	// per-request metadata (Mcp-Method, _meta fields) that is
	// not the subject of this wiring test. The 2025-06-18
	// protocol also matches the "older clients still negotiate
	// 2025-11-25 and earlier" path the README explicitly
	// documents as supported.
	initializeReq, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "riff-mcp-wiring-test",
				"version": "0.0.0",
			},
		},
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(initializeReq))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code,
		"healthy /mcp must answer the initialize handshake; got body=%s", rec.Body.String())

	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), "response must be a valid JSON-RPC envelope: %s", rec.Body.String())
	assert.Equal(t, "2.0", response.JSONRPC)
	assert.Equal(t, 1, response.ID)
	assert.Equal(t, "2025-06-18", response.Result.ProtocolVersion,
		"the SDK must echo the legacy protocol version back when the handshake advertises it")
	assert.Equal(t, "memos", response.Result.ServerInfo.Name,
		"serverInfo.name is hard-coded in NewMCPService and proves the MCP service, not some other handler, answered")
}

// minimalSchema is the small JSON-Schema-shaped fragment the
// fixture uses for every operation's 200 response. Naming the
// type keeps the build literal below readable and ensures the
// response and literal agree on the same shape.
type minimalSchema struct {
	Type string `yaml:"type"`
}

// minimalMediaType is the application/json wrapping around the
// schema. OpenAPI's content map is keyed by media type.
type minimalMediaType struct {
	Schema minimalSchema `yaml:"schema"`
}

// minimalOperationResponse is the response body shape
// writeMinimalOpenAPISpec uses for every operation it emits.
type minimalOperationResponse struct {
	Description string                      `yaml:"description"`
	Content     map[string]minimalMediaType `yaml:"content"`
}

// minimalOperation is the per-operation shape
// writeMinimalOpenAPISpec emits. Only operationId and responses
// are required by the MCP registry; the rest is here to keep the
// output a valid OpenAPI document.
type minimalOperation struct {
	OperationID string                              `yaml:"operationId"`
	Responses   map[string]minimalOperationResponse `yaml:"responses"`
}

// minimalSpec is the document shape writeMinimalOpenAPISpec
// marshals to YAML.
type minimalSpec struct {
	OpenAPI string                                 `yaml:"openapi"`
	Info    map[string]string                      `yaml:"info"`
	Paths   map[string]map[string]minimalOperation `yaml:"paths"`
}

// writeMinimalOpenAPISpec writes a self-contained OpenAPI 3.0
// document to a temp file. The document contains one path per
// curated operation ID, each returning an empty object on 200.
// The MCP service only consults the spec for operationId and
// shape; the bodies, parameters, and schemas below are the
// minimum that satisfy the registry, the curated-list check,
// and validateOperationOverrides.
//
// Generating the fixture from mcp.CuratedOperationIDs() rather
// than hand-maintaining it means the test automatically picks
// up curated-list changes — if a new operation is added without
// covering it here, the wiring test will fail with a clear
// "curated operation X not found" message, pointing at the
// divergence between the catalog and the fixture.
func writeMinimalOpenAPISpec(t *testing.T) string {
	t.Helper()

	doc := minimalSpec{
		OpenAPI: "3.0.0",
		Info: map[string]string{
			"title":   "riff MCP wiring test fixture",
			"version": "0.0.0",
		},
		Paths: map[string]map[string]minimalOperation{},
	}
	for _, op := range mcp.CuratedOperationIDs() {
		doc.Paths["/api/v1/"+op] = map[string]minimalOperation{
			"get": {
				OperationID: op,
				Responses: map[string]minimalOperationResponse{
					"200": {
						Description: "ok",
						Content: map[string]minimalMediaType{
							"application/json": {Schema: minimalSchema{Type: "object"}},
						},
					},
				},
			},
		}
	}

	data, err := yaml.Marshal(doc)
	require.NoError(t, err, "marshal minimal spec")
	path := filepath.Join(t.TempDir(), "openapi.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o600), "write minimal spec")
	return path
}
