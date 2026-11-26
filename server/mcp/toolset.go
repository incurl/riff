package mcp

import (
	"regexp"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/errors"

	"github.com/incurl/riff/internal/mcptools"
)

// Option configures the MCP service.
type Option func(*serviceOptions)

// serviceOptions is what the options accumulate.
type serviceOptions struct {
	declared []mcptools.Tool
}

func newServiceOptions(options []Option) serviceOptions {
	settings := serviceOptions{}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}
	return settings
}

// WithTools adds the tools a MODULE declares for itself.
//
// The catalog is otherwise derived from the generated OpenAPI document,
// which describes the Connect/gRPC surface. The wiki, the forum and Bark are
// hand-written PocketBase surfaces with no OpenAPI document, so each of them
// declares its tools next to the routes they call (`pb/<module>/mcptools.go`)
// and main.go passes them here.
//
// A declared tool becomes the same *sdkmcp.Tool an OpenAPI operation does,
// and executes through the same adapter: the call is dispatched in-process
// against the module's own route with the caller's `Authorization` header,
// so the module's authentication, its access rules and its error shapes are
// the ones that answer. That is the whole point — the MCP surface must not
// be a second, more permissive way into the same data.
func WithTools(tools ...mcptools.Tool) Option {
	return func(settings *serviceOptions) {
		settings.declared = append(settings.declared, tools...)
	}
}

// toolNamePattern is the shape a tool name must have.
//
// snake_case is not cosmetic: an agent picks tools by name, and the curated
// catalog is already snake_case (`memo_list_memos`), so a declared tool that
// read `WikiGetPage` would look like a different kind of thing.
var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// buildDeclaredTools turns module declarations into catalog entries.
//
// Nothing here is lenient: a declaration is code, and a malformed one is a
// startup failure with the tool named. The alternative — a tool that
// registers and then fails at call time — is a catalog entry an agent will
// pick and the server cannot honour.
func buildDeclaredTools(declared []mcptools.Tool) ([]*sdkmcp.Tool, map[string]*registeredOperation, error) {
	tools := make([]*sdkmcp.Tool, 0, len(declared))
	operations := make(map[string]*registeredOperation, len(declared))

	for _, tool := range declared {
		operation, err := declaredOperation(tool)
		if err != nil {
			return nil, nil, err
		}

		built, registered := buildToolWithName(operation, operation.OperationID, tool.Title)
		if _, duplicate := operations[built.Name]; duplicate {
			return nil, nil, errors.Errorf("duplicate MCP tool name %q", built.Name)
		}

		tools = append(tools, built)
		operations[built.Name] = registered
	}
	return tools, operations, nil
}

// declaredOperation validates a declaration and projects it onto the
// operation shape the rest of the service already understands.
func declaredOperation(tool mcptools.Tool) (*openAPIOperation, error) {
	name := strings.TrimSpace(tool.Name)
	if !toolNamePattern.MatchString(name) {
		return nil, errors.Errorf("declared MCP tool %q: the name must be snake_case", tool.Name)
	}

	description := strings.TrimSpace(tool.Description)
	if description == "" {
		return nil, errors.Errorf("declared MCP tool %q: a description is required, because an agent reads it before calling", name)
	}

	method := strings.ToUpper(strings.TrimSpace(tool.Method))
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return nil, errors.Errorf("declared MCP tool %q: unsupported method %q", name, tool.Method)
	}

	path := strings.TrimSpace(tool.Path)
	if !strings.HasPrefix(path, "/") {
		return nil, errors.Errorf("declared MCP tool %q: the path must start with \"/\" (got %q)", name, tool.Path)
	}
	if strings.ContainsAny(path, " ?#") {
		return nil, errors.Errorf("declared MCP tool %q: the path must not carry a query, fragment or space (%q)", name, tool.Path)
	}

	parameters := make([]openAPIParameter, 0, len(tool.Parameters))
	seen := map[string]struct{}{}
	for _, parameter := range tool.Parameters {
		in := strings.ToLower(strings.TrimSpace(parameter.In))
		if in != "path" && in != "query" {
			return nil, errors.Errorf("declared MCP tool %q: parameter %q is in %q; only \"path\" and \"query\" are supported", name, parameter.Name, parameter.In)
		}
		if parameter.Name == "" {
			return nil, errors.Errorf("declared MCP tool %q: a parameter has no name", name)
		}
		key := in + "\x00" + parameter.Name
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.Errorf("declared MCP tool %q: parameter %q is declared twice", name, parameter.Name)
		}
		seen[key] = struct{}{}

		schema := jsonSchema(parameter.Schema)
		if len(schema) == 0 {
			// Every declared parameter is an id, a slug or a search term
			// unless it says otherwise, so a string is the sane default.
			schema = jsonSchema{"type": "string"}
		}

		parameters = append(parameters, openAPIParameter{
			Name:        parameter.Name,
			In:          in,
			Description: parameter.Description,
			// A path parameter is always required: the request cannot be
			// built without it, so marking it optional would offer an
			// argument the server then refuses to do without.
			Required: parameter.Required || in == "path",
			Schema:   schema,
		})
	}

	// Every placeholder needs its parameter, or the tool would register and
	// then answer "missing required path parameter" on every call.
	for _, placeholder := range pathPlaceholderNames(path) {
		if _, ok := seen["path\x00"+placeholder]; !ok {
			return nil, errors.Errorf("declared MCP tool %q: the path placeholder %q has no parameter", name, placeholder)
		}
	}

	operation := &openAPIOperation{
		OperationID: name,
		Description: description,
		Method:      method,
		Path:        path,
		Parameters:  parameters,
	}

	if tool.Body != nil {
		bodySchema := jsonSchema(tool.Body.Schema)
		if len(bodySchema) == 0 {
			bodySchema = jsonSchema{"type": "object"}
		}
		operation.RequestBody = &openAPIRequestBody{Required: tool.Body.Required}
		operation.RequestBodySchema = bodySchema
	}

	return operation, nil
}

// declareCatalog merges the declared tools into the curated catalog.
//
// A name collision is an error rather than a shadowed tool: the client would
// see one tool and the server would keep the other, which is undebuggable
// from the outside.
func declareCatalog(
	curated []*sdkmcp.Tool,
	operationsByTool map[string]*registeredOperation,
	declared []mcptools.Tool,
) ([]*sdkmcp.Tool, map[string]*registeredOperation, error) {
	if len(declared) == 0 {
		return curated, operationsByTool, nil
	}

	tools, operations, err := buildDeclaredTools(declared)
	if err != nil {
		return nil, nil, err
	}

	for name := range operations {
		if _, clash := operationsByTool[name]; clash {
			return nil, nil, errors.Errorf("declared MCP tool %q collides with a curated tool of the same name", name)
		}
	}
	for name, operation := range operations {
		operationsByTool[name] = operation
	}

	return append(curated, tools...), operationsByTool, nil
}
