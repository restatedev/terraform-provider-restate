// Command normalizecloudspec rewrites the vendored Restate Cloud Management API contract into a
// form that an OpenAPI code generator can consume, without editing the pristine snapshot.
//
// The upstream contract declares account-scoped operations under paths such as
// `/{accountId}/DescribeEnvironment` but never declares the `accountId` path parameter on those
// operations. That makes the document invalid: a conforming generator either rejects it or silently
// drops the parameter. Rather than patching the vendored snapshot -- which would destroy its
// provenance and its checksum -- this tool injects the missing declaration as a mechanical,
// deterministic normalization step that runs as part of `go generate`.
//
// The normalization is deliberately narrow. It only ever adds an `accountId` path parameter to an
// operation whose path template contains `{accountId}` and which does not already declare one. Any
// other difference between the pristine and normalized documents is a bug in this tool.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// accountIDParameter is the declaration injected into account-scoped operations. It references the
// contract's own `AccountId` schema so the generated Go type stays consistent with the rest of the
// contract.
const accountIDParameter = `
name: accountId
in: path
required: true
description: |-
  Identifier of the account the operation applies to.

  Injected by tools/cmd/normalizecloudspec: the upstream contract templates this parameter into the
  path but does not declare it.
schema:
  $ref: "#/components/schemas/AccountId"
`

const accountIDPlaceholder = "{accountId}"

func main() {
	in := flag.String("in", "", "path to the pristine OpenAPI document")
	out := flag.String("out", "", "path to write the normalized OpenAPI document to")
	flag.Parse()

	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: normalizecloudspec -in openapi.yaml -out openapi.normalized.yaml")
		os.Exit(2)
	}

	if err := run(*in, *out); err != nil {
		fmt.Fprintf(os.Stderr, "normalizecloudspec: %v\n", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	source, err := os.ReadFile(in)
	if err != nil {
		return err
	}

	normalized, injected, err := Normalize(source)
	if err != nil {
		return err
	}
	if injected == 0 {
		return fmt.Errorf("no account-scoped operation needed an accountId parameter; the upstream contract may have been fixed, so re-evaluate whether this normalization is still required")
	}

	return os.WriteFile(out, normalized, 0o644)
}

// Normalize returns the normalized document and the number of operations it injected a parameter
// into. It is separate from run so tests can exercise it without touching the filesystem.
func Normalize(source []byte) (normalized []byte, injected int, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(source, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse document: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, 0, fmt.Errorf("expected a single YAML document")
	}

	paths := mappingValue(doc.Content[0], "paths")
	if paths == nil {
		return nil, 0, fmt.Errorf("document has no paths object")
	}

	for i := 0; i+1 < len(paths.Content); i += 2 {
		template, pathItem := paths.Content[i].Value, paths.Content[i+1]
		if !strings.Contains(template, accountIDPlaceholder) {
			continue
		}
		added, err := injectIntoPathItem(pathItem)
		if err != nil {
			return nil, 0, fmt.Errorf("path %s: %w", template, err)
		}
		injected += added
	}

	rendered, err := render(&doc)
	if err != nil {
		return nil, 0, err
	}
	return rendered, injected, nil
}

// httpMethods are the operation keys a path item can hold. Anything else -- `parameters`, `summary`,
// vendor extensions -- is left untouched.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

func injectIntoPathItem(pathItem *yaml.Node) (int, error) {
	if pathItem.Kind != yaml.MappingNode {
		return 0, fmt.Errorf("expected a mapping")
	}

	// A parameter declared on the path item itself applies to every operation under it, so an
	// existing path-level declaration means there is nothing to do.
	if declaresAccountID(mappingValue(pathItem, "parameters")) {
		return 0, nil
	}

	injected := 0
	for i := 0; i+1 < len(pathItem.Content); i += 2 {
		key, operation := pathItem.Content[i].Value, pathItem.Content[i+1]
		if !httpMethods[key] {
			continue
		}
		if operation.Kind != yaml.MappingNode {
			return 0, fmt.Errorf("%s: expected a mapping", key)
		}
		if declaresAccountID(mappingValue(operation, "parameters")) {
			continue
		}
		parameter, err := parameterNode()
		if err != nil {
			return 0, err
		}
		if err := appendParameter(operation, parameter); err != nil {
			return 0, fmt.Errorf("%s: %w", key, err)
		}
		injected++
	}
	return injected, nil
}

func appendParameter(operation *yaml.Node, parameter *yaml.Node) error {
	if existing := mappingValue(operation, "parameters"); existing != nil {
		if existing.Kind != yaml.SequenceNode {
			return fmt.Errorf("parameters is not a sequence")
		}
		existing.Content = append(existing.Content, parameter)
		return nil
	}
	operation.Content = append(operation.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "parameters"},
		&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{parameter}},
	)
	return nil
}

func declaresAccountID(parameters *yaml.Node) bool {
	if parameters == nil || parameters.Kind != yaml.SequenceNode {
		return false
	}
	for _, parameter := range parameters.Content {
		name := mappingValue(parameter, "name")
		in := mappingValue(parameter, "in")
		if name != nil && name.Value == "accountId" && in != nil && in.Value == "path" {
			return true
		}
	}
	return false
}

func parameterNode() (*yaml.Node, error) {
	var parsed yaml.Node
	if err := yaml.Unmarshal([]byte(accountIDParameter), &parsed); err != nil {
		return nil, fmt.Errorf("parse injected parameter: %w", err)
	}
	return parsed.Content[0], nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func render(doc *yaml.Node) ([]byte, error) {
	var buf strings.Builder
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode document: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close encoder: %w", err)
	}

	header := "# Code generated by tools/cmd/normalizecloudspec from openapi.yaml. DO NOT EDIT.\n"
	return []byte(header + buf.String()), nil
}
