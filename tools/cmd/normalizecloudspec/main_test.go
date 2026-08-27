package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const minimalSpec = `openapi: 3.1.0
info:
  title: Test
  version: v1
paths:
  /ListAccounts:
    post:
      operationId: ListAccounts
  /{accountId}/DescribeEnvironment:
    post:
      operationId: DescribeEnvironment
components:
  schemas:
    AccountId:
      type: string
`

func TestNormalizeInjectsMissingAccountIDParameter(t *testing.T) {
	out, injected, err := Normalize([]byte(minimalSpec))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if injected != 1 {
		t.Fatalf("injected = %d, want 1", injected)
	}

	parameters := pathParameters(t, out, "/{accountId}/DescribeEnvironment")
	if len(parameters) != 1 {
		t.Fatalf("got %d parameters, want 1", len(parameters))
	}
	got := parameters[0]
	if got.Name != "accountId" || got.In != "path" || !got.Required {
		t.Errorf("parameter = %+v, want a required accountId path parameter", got)
	}
	if got.Schema.Ref != "#/components/schemas/AccountId" {
		t.Errorf("schema $ref = %q, want the contract's AccountId schema", got.Schema.Ref)
	}
}

func TestNormalizeLeavesUnparameterizedPathsAlone(t *testing.T) {
	out, _, err := Normalize([]byte(minimalSpec))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if parameters := pathParameters(t, out, "/ListAccounts"); parameters != nil {
		t.Errorf("/ListAccounts gained parameters %+v", parameters)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	once, _, err := Normalize([]byte(minimalSpec))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	// Re-normalizing an already-normalized document must not add a second declaration. The tool
	// reports zero injections in that case, which run() treats as an error, so assert on the count
	// rather than on run().
	twice, injected, err := Normalize(stripGeneratedHeader(once))
	if err != nil {
		t.Fatalf("Normalize (second pass): %v", err)
	}
	if injected != 0 {
		t.Errorf("second pass injected %d parameters, want 0", injected)
	}
	if string(twice) != string(once) {
		t.Errorf("second pass changed the document")
	}
}

func TestNormalizeRespectsAPathLevelDeclaration(t *testing.T) {
	spec := `openapi: 3.1.0
paths:
  /{accountId}/DescribeEnvironment:
    parameters:
      - name: accountId
        in: path
        required: true
        schema:
          type: string
    post:
      operationId: DescribeEnvironment
`
	_, injected, err := Normalize([]byte(spec))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if injected != 0 {
		t.Errorf("injected = %d, want 0 when the path item already declares the parameter", injected)
	}
}

// TestNormalizeOnlyAddsParameters is the narrowness guarantee PROVENANCE.md claims: the normalized
// document must differ from the pristine one only by injected `parameters` declarations.
func TestNormalizeOnlyAddsParameters(t *testing.T) {
	out, _, err := Normalize([]byte(minimalSpec))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	before := decode(t, []byte(minimalSpec))
	after := decode(t, out)

	stripInjected(before)
	stripInjected(after)

	if got, want := remarshal(t, after), remarshal(t, before); got != want {
		t.Errorf("normalization changed more than the injected parameters:\n--- pristine ---\n%s\n--- normalized ---\n%s", want, got)
	}
}

func TestNormalizeRejectsANonDocument(t *testing.T) {
	if _, _, err := Normalize([]byte("- not: a document\n")); err == nil {
		t.Fatal("Normalize accepted a non-mapping document")
	}
}

func TestNormalizeRejectsADocumentWithoutPaths(t *testing.T) {
	if _, _, err := Normalize([]byte("openapi: 3.1.0\n")); err == nil {
		t.Fatal("Normalize accepted a document with no paths object")
	}
}

type parameter struct {
	Name     string `yaml:"name"`
	In       string `yaml:"in"`
	Required bool   `yaml:"required"`
	Schema   struct {
		Ref string `yaml:"$ref"`
	} `yaml:"schema"`
}

func pathParameters(t *testing.T, document []byte, template string) []parameter {
	t.Helper()
	var doc struct {
		Paths map[string]struct {
			Post struct {
				Parameters []parameter `yaml:"parameters"`
			} `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(document, &doc); err != nil {
		t.Fatalf("unmarshal normalized document: %v", err)
	}
	item, ok := doc.Paths[template]
	if !ok {
		t.Fatalf("normalized document has no path %q", template)
	}
	return item.Post.Parameters
}

func decode(t *testing.T, document []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := yaml.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return decoded
}

// stripInjected removes every operation-level `parameters` key so the remainder of the two
// documents can be compared for equality.
func stripInjected(document map[string]any) {
	paths, _ := document["paths"].(map[string]any)
	for _, item := range paths {
		operations, _ := item.(map[string]any)
		for _, operation := range operations {
			if body, ok := operation.(map[string]any); ok {
				delete(body, "parameters")
			}
		}
	}
}

func remarshal(t *testing.T, document map[string]any) string {
	t.Helper()
	out, err := yaml.Marshal(document)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

func stripGeneratedHeader(document []byte) []byte {
	_, rest, found := strings.Cut(string(document), "\n")
	if !found {
		return document
	}
	return []byte(rest)
}
