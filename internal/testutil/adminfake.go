package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// AdminFake is a stateful model of the per-environment Admin API's deployment, Kafka cluster and
// subscription lifecycles.
//
// It exists for the same reason CloudFake does, plus one more: the provider verifies that the API
// applied what it was asked for, because Terraform requires the value after an apply to equal the
// plan. A static fixture that always reports its original values is indistinguishable from a server
// that ignored the request, so it would make a correct provider fail its own consistency check.
//
// The fake models only what the provider depends on, and it redacts sensitive Kafka properties on
// read exactly as the real API does -- otherwise the provider's redaction handling would go
// untested.
type AdminFake struct {
	*APIServer

	mu sync.Mutex

	deploymentCounter   int
	subscriptionCounter int

	deployments   map[string]*FakeDeployment
	kafkaClusters map[string]*FakeKafkaCluster
	subscriptions map[string]*FakeSubscription

	// Version is what GET /version reports.
	Version                                string
	MinAdminAPIVersion, MaxAdminAPIVersion int
	IngressEndpoint                        string

	// DiscoveredServices are the services registration reports for a new deployment.
	DiscoveredServices []string

	// DiscoveryError, when set, makes registration fail the way an unreachable endpoint does.
	DiscoveryError string
	// CreatedDeploymentURIOverride and CreatedKafkaNameOverride simulate a successful write whose
	// response disagrees with the request, for partial-state cleanup tests.
	CreatedDeploymentURIOverride string
	CreatedKafkaNameOverride     string
	// CreatedDeploymentAuthOverride is the raw `auth` object a registration reports back, for testing
	// a server that fills in authentication details the request left for it to derive.
	CreatedDeploymentAuthOverride string

	// RedactionMarker is substituted for a sensitive Kafka property value on read.
	RedactionMarker string
}

// FakeDeployment is one registered deployment.
type FakeDeployment struct {
	ID   string
	Kind string

	URI           string
	UseHTTP11     bool
	Auth          json.RawMessage
	ARN           string
	AssumeRoleARN string

	AdditionalHeaders map[string]string
	Metadata          map[string]string
	Services          []string
}

// FakeKafkaCluster is one Kafka cluster configuration.
type FakeKafkaCluster struct {
	Name       string
	Properties map[string]string
}

// FakeSubscription is one subscription.
type FakeSubscription struct {
	ID      string
	Source  string
	Sink    string
	Options map[string]string
}

// NewAdminFake starts a fake Admin API.
func NewAdminFake(t *testing.T) *AdminFake {
	t.Helper()

	fake := &AdminFake{
		APIServer:          NewAPIServer(t),
		deployments:        map[string]*FakeDeployment{},
		kafkaClusters:      map[string]*FakeKafkaCluster{},
		subscriptions:      map[string]*FakeSubscription{},
		Version:            "1.7.6",
		MinAdminAPIVersion: 2,
		MaxAdminAPIVersion: 4,
		IngressEndpoint:    "https://env_1.env.us.restate.cloud",
		DiscoveredServices: []string{"OrderService"},
		RedactionMarker:    "***",
	}
	// The Admin API is REST-shaped: /deployments and /deployments/<id> are different operations.
	fake.UseFullPath = true
	fake.register()
	return fake
}

// Deployment returns a deployment's state so a test can inspect or mutate it, which is how
// out-of-band drift is simulated.
func (f *AdminFake) Deployment(id string) *FakeDeployment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployments[id]
}

// KafkaCluster returns a cluster's state.
func (f *AdminFake) KafkaCluster(name string) *FakeKafkaCluster {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kafkaClusters[name]
}

// Subscription returns a subscription's state.
func (f *AdminFake) Subscription(id string) *FakeSubscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscriptions[id]
}

// DeleteDeployment removes a deployment behind the provider's back.
func (f *AdminFake) DeleteDeployment(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.deployments, id)
}

// DeleteKafkaCluster removes a cluster behind the provider's back.
func (f *AdminFake) DeleteKafkaCluster(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.kafkaClusters, name)
}

// DeleteSubscription removes a subscription behind the provider's back.
func (f *AdminFake) DeleteSubscription(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subscriptions, id)
}

// Counts of the live objects, for asserting that a destroy happened.
func (f *AdminFake) DeploymentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deployments)
}

func (f *AdminFake) KafkaClusterCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.kafkaClusters)
}

func (f *AdminFake) SubscriptionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subscriptions)
}

func (f *AdminFake) register() {
	f.OnFunc("/version", func(Request) Response {
		f.mu.Lock()
		defer f.mu.Unlock()
		return jsonResponse(http.StatusOK, map[string]any{
			"version":               f.Version,
			"min_admin_api_version": f.MinAdminAPIVersion,
			"max_admin_api_version": f.MaxAdminAPIVersion,
			"ingress_endpoint":      f.IngressEndpoint,
			"features":              map[string]bool{"vqueues": false},
		})
	})
	f.OnFunc("/health", func(Request) Response { return Response{Status: http.StatusOK} })

	f.OnFunc("/deployments", func(request Request) Response {
		if request.Method == http.MethodPost {
			return f.registerDeployment(request)
		}
		return f.listDeployments()
	})
	f.OnFunc("/kafka-clusters", func(request Request) Response {
		if request.Method == http.MethodPost {
			return f.createKafkaCluster(request)
		}
		return f.listKafkaClusters()
	})
	f.OnFunc("/subscriptions", func(request Request) Response {
		if request.Method == http.MethodPost {
			return f.createSubscription(request)
		}
		return f.listSubscriptions()
	})

	// The per-object routes are matched by prefix, because their identifiers are part of the path.
	f.Fallback = f.routeByPath
}

// routeByPath handles the paths whose last segment is an identifier rather than a fixed name.
func (f *AdminFake) routeByPath(request Request) Response {
	path := request.Path

	if id, ok := trimPrefix(path, "/deployments/"); ok {
		switch request.Method {
		case http.MethodGet:
			return f.getDeployment(id)
		case http.MethodPatch:
			return f.updateDeployment(id, request)
		case http.MethodDelete:
			return f.deleteDeployment(id, request)
		}
	}
	if name, ok := trimPrefix(path, "/kafka-clusters/"); ok {
		switch request.Method {
		case http.MethodGet:
			return f.getKafkaCluster(name, request)
		case http.MethodPatch:
			return f.updateKafkaCluster(name, request)
		case http.MethodDelete:
			return f.deleteKafkaCluster(name, request)
		}
	}
	if id, ok := trimPrefix(path, "/subscriptions/"); ok {
		switch request.Method {
		case http.MethodGet:
			return f.getSubscription(id)
		case http.MethodDelete:
			return f.deleteSubscription(id)
		}
	}

	return adminError(http.StatusNotFound, fmt.Sprintf("the fake Admin API has no route for %s %s", request.Method, path))
}

func trimPrefix(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

func (f *AdminFake) registerDeployment(request Request) Response {
	if f.DiscoveryError != "" {
		return adminError(http.StatusBadRequest, "failed to discover deployment: "+f.DiscoveryError)
	}

	var body struct {
		URI               *string           `json:"uri"`
		UseHTTP11         *bool             `json:"use_http_11"`
		Auth              json.RawMessage   `json:"auth"`
		ARN               *string           `json:"arn"`
		AssumeRoleARN     *string           `json:"assume_role_arn"`
		AdditionalHeaders map[string]string `json:"additional_headers"`
		Metadata          map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return adminError(http.StatusBadRequest, "malformed request body")
	}
	if (body.URI == nil) == (body.ARN == nil) {
		// The untagged union has exactly one required discriminating field; a body with both or
		// neither matches no member, and a real server would reject it.
		return adminError(http.StatusBadRequest, "a deployment must be either an HTTP or a Lambda endpoint")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.deploymentCounter++
	deployment := &FakeDeployment{
		ID:                fmt.Sprintf("dp_%d", f.deploymentCounter),
		AdditionalHeaders: body.AdditionalHeaders,
		Metadata:          body.Metadata,
		Services:          append([]string(nil), f.DiscoveredServices...),
	}
	if body.URI != nil {
		deployment.Kind = "http"
		deployment.URI = *body.URI
		if f.CreatedDeploymentURIOverride != "" {
			deployment.URI = f.CreatedDeploymentURIOverride
		}
		deployment.Auth = body.Auth
		if f.CreatedDeploymentAuthOverride != "" {
			deployment.Auth = json.RawMessage(f.CreatedDeploymentAuthOverride)
		}
		if body.UseHTTP11 != nil {
			deployment.UseHTTP11 = *body.UseHTTP11
		}
	} else {
		deployment.Kind = "lambda"
		deployment.ARN = *body.ARN
		if body.AssumeRoleARN != nil {
			deployment.AssumeRoleARN = *body.AssumeRoleARN
		}
	}
	f.deployments[deployment.ID] = deployment

	services := make([]map[string]any, 0, len(deployment.Services))
	for _, name := range deployment.Services {
		services = append(services, map[string]any{
			"name": name, "ty": "Service", "revision": 1,
			"deployment_id": deployment.ID, "handlers": []any{},
		})
	}
	return jsonResponse(http.StatusCreated, map[string]any{
		"id":                   deployment.ID,
		"min_protocol_version": 5,
		"max_protocol_version": 6,
		"sdk_version":          "typescript-1.9.0",
		"services":             services,
	})
}

func (f *AdminFake) getDeployment(id string) Response {
	f.mu.Lock()
	defer f.mu.Unlock()

	deployment, ok := f.deployments[id]
	if !ok {
		return adminError(http.StatusNotFound, "deployment not found")
	}
	return jsonResponse(http.StatusOK, f.deploymentBody(deployment))
}

// deploymentBody renders a deployment. The caller must hold the lock.
func (f *AdminFake) deploymentBody(deployment *FakeDeployment) map[string]any {
	services := make([]map[string]any, 0, len(deployment.Services))
	for _, name := range deployment.Services {
		services = append(services, map[string]any{"name": name, "revision": 1})
	}

	body := map[string]any{
		"id":                   deployment.ID,
		"created_at":           "2026-08-27T10:00:00Z",
		"min_protocol_version": 5,
		"max_protocol_version": 6,
		"sdk_version":          "typescript-1.9.0",
		"services":             services,
	}
	if deployment.AdditionalHeaders != nil {
		body["additional_headers"] = deployment.AdditionalHeaders
	}
	if deployment.Metadata != nil {
		body["metadata"] = deployment.Metadata
	}

	if deployment.Kind == "http" {
		body["uri"] = deployment.URI
		// The negotiated version, which is an outcome rather than the read-back of use_http_11.
		body["http_version"] = "HTTP/2"
		body["protocol_type"] = "BidiStream"
		if deployment.UseHTTP11 {
			body["http_version"] = "HTTP/1.1"
			body["protocol_type"] = "RequestResponse"
		}
		if len(deployment.Auth) > 0 && string(deployment.Auth) != "null" {
			body["auth"] = deployment.Auth
		}
		return body
	}

	body["arn"] = deployment.ARN
	body["compression"] = "Zstd"
	if deployment.AssumeRoleARN != "" {
		body["assume_role_arn"] = deployment.AssumeRoleARN
	}
	return body
}

func (f *AdminFake) listDeployments() Response {
	f.mu.Lock()
	defer f.mu.Unlock()

	deployments := make([]map[string]any, 0, len(f.deployments))
	for _, deployment := range f.deployments {
		deployments = append(deployments, f.deploymentBody(deployment))
	}
	return jsonResponse(http.StatusOK, map[string]any{"deployments": deployments})
}

func (f *AdminFake) updateDeployment(id string, request Request) Response {
	var body struct {
		URI               *string           `json:"uri"`
		UseHTTP11         *bool             `json:"use_http_11"`
		ARN               *string           `json:"arn"`
		AssumeRoleARN     json.RawMessage   `json:"assume_role_arn"`
		AdditionalHeaders map[string]string `json:"additional_headers"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return adminError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	deployment, ok := f.deployments[id]
	if !ok {
		return adminError(http.StatusNotFound, "deployment not found")
	}
	if body.URI != nil {
		deployment.URI = *body.URI
	}
	if body.UseHTTP11 != nil {
		deployment.UseHTTP11 = *body.UseHTTP11
	}
	if body.ARN != nil {
		deployment.ARN = *body.ARN
	}
	if body.AssumeRoleARN != nil {
		if string(body.AssumeRoleARN) == "null" {
			deployment.AssumeRoleARN = ""
		} else if err := json.Unmarshal(body.AssumeRoleARN, &deployment.AssumeRoleARN); err != nil {
			return adminError(http.StatusBadRequest, "malformed assume_role_arn")
		}
	}
	if body.AdditionalHeaders != nil {
		deployment.AdditionalHeaders = body.AdditionalHeaders
	}
	return jsonResponse(http.StatusOK, f.deploymentBody(deployment))
}

func (f *AdminFake) deleteDeployment(id string, request Request) Response {
	// The API implements only forced deletion, so a delete without it would be rejected. The provider
	// refuses before sending one, and this asserts the parameter is present when it does.
	if !strings.Contains(request.Query, "force=true") {
		return adminError(http.StatusBadRequest, "only forced deletion is implemented")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.deployments[id]; !ok {
		return adminError(http.StatusNotFound, "deployment not found")
	}
	delete(f.deployments, id)
	return Response{Status: http.StatusAccepted}
}

func (f *AdminFake) createKafkaCluster(request Request) Response {
	var body struct {
		Name       string            `json:"name"`
		Properties map[string]string `json:"properties"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return adminError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	name := body.Name
	if f.CreatedKafkaNameOverride != "" {
		name = f.CreatedKafkaNameOverride
	}
	if _, exists := f.kafkaClusters[name]; exists {
		return adminError(http.StatusConflict, "a Kafka cluster with this name already exists")
	}
	cluster := &FakeKafkaCluster{Name: name, Properties: body.Properties}
	f.kafkaClusters[name] = cluster
	return jsonResponse(http.StatusCreated, f.kafkaClusterBody(cluster, false))
}

// kafkaClusterBody renders a cluster with sensitive values redacted, as the real API does. The caller
// must hold the lock.
func (f *AdminFake) kafkaClusterBody(cluster *FakeKafkaCluster, includeSubscriptions bool) map[string]any {
	properties := make(map[string]string, len(cluster.Properties))
	for name, value := range cluster.Properties {
		if isSensitiveKafkaProperty(name) {
			properties[name] = f.RedactionMarker
			continue
		}
		properties[name] = value
	}

	body := map[string]any{
		"name":       cluster.Name,
		"properties": properties,
		"created_at": "2026-08-27T10:00:00Z",
	}
	if includeSubscriptions {
		subscriptions := make([]map[string]any, 0)
		for _, subscription := range f.subscriptions {
			if subscriptionCluster(subscription.Source) == cluster.Name {
				subscriptions = append(subscriptions, subscriptionBody(subscription))
			}
		}
		body["subscriptions"] = subscriptions
	}
	return body
}

func (f *AdminFake) getKafkaCluster(name string, request Request) Response {
	f.mu.Lock()
	defer f.mu.Unlock()

	cluster, ok := f.kafkaClusters[name]
	if !ok {
		return adminError(http.StatusNotFound, "Kafka cluster not found")
	}
	return jsonResponse(http.StatusOK, f.kafkaClusterBody(cluster, strings.Contains(request.Query, "include_subscriptions=true")))
}

func (f *AdminFake) listKafkaClusters() Response {
	f.mu.Lock()
	defer f.mu.Unlock()

	clusters := make([]map[string]any, 0, len(f.kafkaClusters))
	for _, cluster := range f.kafkaClusters {
		clusters = append(clusters, f.kafkaClusterBody(cluster, false))
	}
	return jsonResponse(http.StatusOK, map[string]any{"clusters": clusters})
}

func (f *AdminFake) updateKafkaCluster(name string, request Request) Response {
	var body struct {
		Properties map[string]string `json:"properties"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return adminError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	cluster, ok := f.kafkaClusters[name]
	if !ok {
		return adminError(http.StatusNotFound, "Kafka cluster not found")
	}
	// The update replaces the whole map, so a property omitted from a later apply is removed.
	cluster.Properties = body.Properties
	return jsonResponse(http.StatusOK, f.kafkaClusterBody(cluster, false))
}

func (f *AdminFake) deleteKafkaCluster(name string, request Request) Response {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.kafkaClusters[name]; !ok {
		return adminError(http.StatusNotFound, "Kafka cluster not found")
	}

	// Without force, the server's own dependency check refuses to orphan subscriptions.
	if !strings.Contains(request.Query, "force=true") {
		for _, subscription := range f.subscriptions {
			if subscriptionCluster(subscription.Source) == name {
				return adminError(http.StatusConflict, "the Kafka cluster still has subscriptions referencing it")
			}
		}
	}

	delete(f.kafkaClusters, name)
	return Response{Status: http.StatusAccepted}
}

func (f *AdminFake) createSubscription(request Request) Response {
	var body struct {
		Source  string            `json:"source"`
		Sink    string            `json:"sink"`
		Options map[string]string `json:"options"`
	}
	if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
		return adminError(http.StatusBadRequest, "malformed request body")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.subscriptionCounter++
	subscription := &FakeSubscription{
		ID:      fmt.Sprintf("sub_%d", f.subscriptionCounter),
		Source:  body.Source,
		Sink:    body.Sink,
		Options: body.Options,
	}
	f.subscriptions[subscription.ID] = subscription
	return jsonResponse(http.StatusCreated, subscriptionBody(subscription))
}

func (f *AdminFake) getSubscription(id string) Response {
	f.mu.Lock()
	defer f.mu.Unlock()

	subscription, ok := f.subscriptions[id]
	if !ok {
		return adminError(http.StatusNotFound, "subscription not found")
	}
	return jsonResponse(http.StatusOK, subscriptionBody(subscription))
}

func (f *AdminFake) listSubscriptions() Response {
	f.mu.Lock()
	defer f.mu.Unlock()

	subscriptions := make([]map[string]any, 0, len(f.subscriptions))
	for _, subscription := range f.subscriptions {
		subscriptions = append(subscriptions, subscriptionBody(subscription))
	}
	return jsonResponse(http.StatusOK, map[string]any{"subscriptions": subscriptions})
}

func (f *AdminFake) deleteSubscription(id string) Response {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.subscriptions[id]; !ok {
		return adminError(http.StatusNotFound, "subscription not found")
	}
	delete(f.subscriptions, id)
	return Response{Status: http.StatusAccepted}
}

func subscriptionBody(subscription *FakeSubscription) map[string]any {
	options := subscription.Options
	if options == nil {
		options = map[string]string{}
	}
	return map[string]any{
		"id":      subscription.ID,
		"source":  subscription.Source,
		"sink":    subscription.Sink,
		"options": options,
	}
}

// subscriptionCluster extracts the cluster name from a kafka://<cluster>/<topic> source.
func subscriptionCluster(source string) string {
	rest, ok := strings.CutPrefix(source, "kafka://")
	if !ok {
		return ""
	}
	cluster, _, _ := strings.Cut(rest, "/")
	return cluster
}

// isSensitiveKafkaProperty mirrors the real API's redaction. It is deliberately a small, independent
// list rather than a call into the provider's own matcher: a test fixture that shares the code under
// test cannot catch a mistake in it.
func isSensitiveKafkaProperty(name string) bool {
	lower := strings.ToLower(name)
	for _, needle := range []string{"password", "secret", "sasl.username", "ssl.key.pem"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func adminError(status int, message string) Response {
	encoded, err := json.Marshal(map[string]any{"message": message})
	if err != nil {
		panic(err)
	}
	return Response{Status: status, Body: string(encoded)}
}
