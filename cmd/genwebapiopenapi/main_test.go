package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mjl-/mox/webapi"
)

func TestGenerate(t *testing.T) {
	doc, err := generate()
	if err != nil {
		t.Fatal(err)
	}

	if got, want := doc["openapi"], "3.1.0"; got != want {
		t.Fatalf("openapi version: got %v, want %v", got, want)
	}
	paths := doc["paths"].(map[string]any)
	methods := reflect.TypeFor[webapi.Methods]().NumMethod()
	if got := len(paths); got != methods {
		t.Fatalf("paths: got %d, want %d", got, methods)
	}

	send := paths["/webapi/v0/Send"].(map[string]any)["post"].(map[string]any)
	requestBody := send["requestBody"].(map[string]any)
	content := requestBody["content"].(map[string]any)
	form := content["application/x-www-form-urlencoded"].(map[string]any)["schema"].(map[string]any)
	formProperties := form["properties"].(map[string]any)
	if _, ok := formProperties["inlinefile"]; ok {
		t.Fatal("multipart file field leaked into form schema")
	}
	multipartSchema := content["multipart/form-data"].(map[string]any)["schema"].(map[string]any)
	multipartProperties := multipartSchema["properties"].(map[string]any)
	if _, ok := multipartProperties["inlinefile"]; !ok {
		t.Fatal("multipart schema missing inlinefile")
	}

	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	sendSchema := schemas["SendRequest"].(map[string]any)
	sendProperties := sendSchema["properties"].(map[string]any)
	if _, ok := sendProperties["To"]; !ok {
		t.Fatal("SendRequest did not flatten Message fields")
	}
	if _, ok := schemas["Structure"]; !ok {
		t.Fatal("recursive webhook.Structure schema missing")
	}

	binaryMethods := []string{"MessageRawGet", "MessagePartGet"}
	for _, method := range binaryMethods {
		operation := paths["/webapi/v0/"+method].(map[string]any)["post"].(map[string]any)
		responses := operation["responses"].(map[string]any)
		response200 := responses["200"].(map[string]any)
		responseContent := response200["content"].(map[string]any)
		if _, ok := responseContent["application/octet-stream"]; !ok {
			t.Errorf("%s is missing binary response", method)
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	first, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("generated documents differ")
	}
}
