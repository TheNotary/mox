package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mjl-/mox/webapi"
)

var outputPath = flag.String("output", "webapi/openapi.json", "path to the generated OpenAPI document")

var (
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType   = reflect.TypeOf((*error)(nil)).Elem()
	readCloser  = reflect.TypeOf((*io.ReadCloser)(nil)).Elem()
	timeType    = reflect.TypeOf(time.Time{})
)

type generator struct {
	schemas map[string]any
	seen    map[reflect.Type]bool
}

func main() {
	flag.Parse()
	doc, err := generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate OpenAPI: %v\n", err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(doc, "", "\t")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal OpenAPI: %v\n", err)
		os.Exit(1)
	}
	data = append(data, '\n')
	if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write OpenAPI: %v\n", err)
		os.Exit(1)
	}
}

func generate() (map[string]any, error) {
	g := &generator{schemas: map[string]any{}, seen: map[reflect.Type]bool{}}
	methods := reflect.TypeFor[webapi.Methods]()
	paths := map[string]any{}
	methodNames := make([]string, methods.NumMethod())
	for i := range methods.NumMethod() {
		methodNames[i] = methods.Method(i).Name
	}
	sort.Strings(methodNames)

	for _, name := range methodNames {
		method, ok := methods.MethodByName(name)
		if !ok {
			return nil, fmt.Errorf("method %s disappeared", name)
		}
		if method.Type.NumIn() != 2 || method.Type.NumOut() != 2 || method.Type.In(0) != contextType || method.Type.Out(1) != errorType {
			return nil, fmt.Errorf("unsupported Methods.%s signature: %s", name, method.Type)
		}
		requestType := method.Type.In(1)
		responseType := method.Type.Out(0)
		requestSchema, err := g.schema(requestType)
		if err != nil {
			return nil, fmt.Errorf("Methods.%s request: %w", name, err)
		}

		responses := map[string]any{
			"200": map[string]any{
				"description": "Successful response",
			},
			"400": map[string]any{
				"description": "The request was invalid or could not be processed",
				"content": map[string]any{
					"application/json": map[string]any{"schema": g.refSchema("Error")},
				},
			},
			"401": map[string]any{"description": "Authentication failed"},
			"429": map[string]any{"description": "Too many authentication attempts"},
		}
		if responseType.Implements(readCloser) {
			responses["200"] = map[string]any{
				"description": "Successful response containing message data",
				"content": map[string]any{
					"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}},
				},
			}
		} else {
			responseSchema, err := g.schema(responseType)
			if err != nil {
				return nil, fmt.Errorf("Methods.%s response: %w", name, err)
			}
			responses["200"] = map[string]any{
				"description": "Successful response",
				"content": map[string]any{
					"application/json": map[string]any{"schema": responseSchema},
				},
			}
		}

		requestDescription := "JSON request object encoded as the form field `request`."
		if name == "Send" {
			requestDescription += " Multipart requests may also include repeated `alternativefile`, `inlinefile`, and `attachedfile` file parts."
		}
		formSchema := map[string]any{
			"type":     "object",
			"required": []string{"request"},
			"properties": map[string]any{
				"request": map[string]any{
					"type":             "string",
					"description":      requestDescription,
					"contentMediaType": "application/json",
					"contentSchema":    requestSchema,
				},
			},
		}
		content := map[string]any{
			"application/x-www-form-urlencoded": map[string]any{"schema": formSchema},
		}
		if name == "Send" {
			multipartSchema := cloneMap(formSchema)
			properties := multipartSchema["properties"].(map[string]any)
			for _, field := range []string{"alternativefile", "inlinefile", "attachedfile"} {
				properties[field] = map[string]any{"type": "string", "format": "binary"}
			}
			content["multipart/form-data"] = map[string]any{"schema": multipartSchema}
		}

		paths["/webapi/v0/"+name] = map[string]any{
			"post": map[string]any{
				"operationId": name,
				"description": "Call the Mox WebAPI method " + name + ".",
				"security":    []any{map[string]any{"basicAuth": []string{}}},
				"requestBody": map[string]any{"required": true, "content": content},
				"responses":   responses,
			},
		}
	}

	g.schemas["Error"] = map[string]any{
		"type":        "object",
		"description": "Error returned with HTTP status 400.",
		"required":    []string{"Code", "Message"},
		"properties": map[string]any{
			"Code":    map[string]any{"type": "string"},
			"Message": map[string]any{"type": "string"},
		},
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":        "Mox WebAPI",
			"version":      "0",
			descriptionKey: "HTTP Basic authenticated API for sending and managing messages. The configured WebAPI path may differ from the default /webapi/v0/.",
		},
		"servers": []any{map[string]any{"url": "/webapi/v0"}},
		"paths":   paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{"basicAuth": map[string]any{"type": "http", "scheme": "basic", "description": "Use the account email address as the username."}},
			"schemas":         g.schemas,
		},
	}, nil
}

const descriptionKey = "description"

func (g *generator) schema(t reflect.Type) (any, error) {
	for t.Kind() == reflect.Pointer {
		s, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}, nil
	}
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer", "format": "int64"}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer", "format": "int64"}, nil
	case reflect.Float32:
		return map[string]any{"type": "number", "format": "float"}, nil
	case reflect.Float64:
		return map[string]any{"type": "number", "format": "double"}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Slice, reflect.Array:
		items, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map key %s is not a string", t.Key())
		}
		additional, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": additional}, nil
	case reflect.Interface:
		return map[string]any{}, nil
	case reflect.Struct:
		return g.structSchema(t)
	default:
		return nil, fmt.Errorf("unsupported type %s", t)
	}
}

func (g *generator) structSchema(t reflect.Type) (any, error) {
	name := t.Name()
	if name == "" {
		return nil, fmt.Errorf("anonymous struct is unsupported")
	}
	if g.seen[t] {
		return g.refSchema(name), nil
	}
	g.seen[t] = true
	g.schemas[name] = map[string]any{"type": "object", "properties": map[string]any{}}
	properties := g.schemas[name].(map[string]any)["properties"].(map[string]any)
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		jsonName, skip := jsonFieldName(field)
		if skip {
			continue
		}
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			embedded, err := g.schema(field.Type)
			if err != nil {
				return nil, err
			}
			if ref, ok := embedded.(map[string]any)["$ref"].(string); ok {
				embeddedName := strings.TrimPrefix(ref, "#/components/schemas/")
				embeddedSchema := g.schemas[embeddedName].(map[string]any)
				for key, value := range embeddedSchema["properties"].(map[string]any) {
					properties[key] = value
				}
				continue
			}
		}
		value, err := g.schema(field.Type)
		if err != nil {
			return nil, fmt.Errorf("field %s.%s: %w", t, field.Name, err)
		}
		properties[jsonName] = value
	}
	return g.refSchema(name), nil
}

func (g *generator) refSchema(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", true
	}
	if comma := strings.IndexByte(tag, ','); comma >= 0 {
		tag = tag[:comma]
	}
	if tag != "" {
		return tag, false
	}
	return field.Name, false
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	if properties, ok := input["properties"].(map[string]any); ok {
		clonedProperties := make(map[string]any, len(properties))
		for key, value := range properties {
			clonedProperties[key] = value
		}
		output["properties"] = clonedProperties
	}
	return output
}
