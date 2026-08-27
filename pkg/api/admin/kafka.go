package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin/internal/generated"
)

// KafkaClusters is the endpoint adapter for Kafka cluster configurations.
type KafkaClusters struct{ client *Client }

// KafkaClusters returns the Kafka cluster adapter.
func (c *Client) KafkaClusters() *KafkaClusters { return &KafkaClusters{client: c} }

// Kafka clusters are addressed by name, so the identity type is the name and Update cannot rename.
var (
	_ api.Creator[KafkaCluster, KafkaCluster]              = (*KafkaClusters)(nil)
	_ api.Getter[string, KafkaCluster]                     = (*KafkaClusters)(nil)
	_ api.Updater[string, map[string]string, KafkaCluster] = (*KafkaClusters)(nil)
	_ api.Deleter[string, DeleteKafkaClusterOptions]       = (*KafkaClusters)(nil)
	_ api.Lister[KafkaCluster]                             = (*KafkaClusters)(nil)
)

// RedactionMarkers are the values the Admin API substitutes for a sensitive Kafka property on read.
//
// The contract documents that sensitive properties are redacted but does not specify the marker, so
// this list is deliberately broad and is treated as a heuristic. MergeRedactedProperties therefore
// falls back on the property's sensitivity rather than trusting the marker alone.
var RedactionMarkers = []string{
	"***",
	"*****",
	"[REDACTED]",
	"<redacted>",
	"redacted",
}

// IsRedactionMarker reports whether a value read back from the API looks like a redaction placeholder
// rather than a real property value.
func IsRedactionMarker(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	for _, marker := range RedactionMarkers {
		if strings.EqualFold(trimmed, marker) {
			return true
		}
	}
	// A value that is nothing but asterisks is a placeholder in every convention.
	return strings.Trim(trimmed, "*") == "" && len(trimmed) >= 3
}

// MergeRedactedProperties reconciles the properties the API returned with the properties the caller
// already knows it configured.
//
// The API redacts sensitive property values on read, so a naive refresh would either overwrite a
// real credential with a placeholder or report a permanent diff. For each property:
//
//   - A value the API redacted, or any value of a property whose name is sensitive, keeps the
//     configured value when there is one. The API is not a source of truth for a value it will not
//     disclose.
//   - Everything else takes the API's value, so genuine out-of-band changes to non-secret properties
//     are still detected as drift.
//   - A property the API no longer reports is dropped, because that is a real removal.
//   - A property the API reports that the caller did not configure is added, because that is a real
//     addition.
//
// When configured is nil -- an import, where nothing is known -- the API's values are returned
// as-is, redaction markers included. The caller must document that a redacted property has to be
// supplied before the next apply.
func MergeRedactedProperties(remote, configured map[string]string) map[string]string {
	if remote == nil {
		return nil
	}

	merged := make(map[string]string, len(remote))
	for name, remoteValue := range remote {
		configuredValue, wasConfigured := configured[name]
		if !wasConfigured {
			merged[name] = remoteValue
			continue
		}
		// Prefer the configured value whenever the API cannot be trusted to have disclosed the real
		// one: either it visibly redacted this value, or the property is one whose values are
		// secret and whose marker this snapshot may not recognise.
		if IsRedactionMarker(remoteValue) || api.IsSensitivePropertyName(name) {
			merged[name] = configuredValue
			continue
		}
		merged[name] = remoteValue
	}
	return merged
}

// Create registers a Kafka cluster configuration.
//
// Retry safety is RetryThrottleOnly: creating a cluster that already exists is a conflict, and a
// retry after an ambiguous 5xx would report that conflict instead of the original failure.
func (k *KafkaClusters) Create(ctx context.Context, cluster KafkaCluster) (KafkaCluster, error) {
	if cluster.Name == "" {
		return KafkaCluster{}, errors.New("a Kafka cluster name is required")
	}
	if err := ValidateKafkaProperties(cluster.Properties); err != nil {
		return KafkaCluster{}, err
	}

	properties := make(map[string]string, len(cluster.Properties))
	for name, value := range cluster.Properties {
		properties[name] = value
	}

	response, err := api.Execute[generated.KafkaClusterResponse](ctx, k.client.executor, "CreateKafkaCluster", api.RetryThrottleOnly,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.CreateKafkaCluster(ctx, generated.CreateKafkaClusterJSONRequestBody{
				Name:       cluster.Name,
				Properties: properties,
			})
		})
	if err != nil {
		return KafkaCluster{}, err
	}
	return kafkaClusterFrom(response), nil
}

// Get reads one Kafka cluster.
//
// Sensitive property values come back redacted. Callers reconciling state must pass the result
// through MergeRedactedProperties along with what they configured.
func (k *KafkaClusters) Get(ctx context.Context, name string) (KafkaCluster, error) {
	return k.get(ctx, name, false)
}

// GetWithSubscriptions reads one Kafka cluster along with the subscriptions that reference it, which
// is what makes a dependency-safe delete decision possible.
func (k *KafkaClusters) GetWithSubscriptions(ctx context.Context, name string) (KafkaCluster, error) {
	return k.get(ctx, name, true)
}

func (k *KafkaClusters) get(ctx context.Context, name string, includeSubscriptions bool) (KafkaCluster, error) {
	if name == "" {
		return KafkaCluster{}, errors.New("a Kafka cluster name is required")
	}

	params := &generated.GetKafkaClusterParams{}
	if includeSubscriptions {
		params.IncludeSubscriptions = &includeSubscriptions
	}

	response, err := api.Execute[generated.KafkaClusterResponse](ctx, k.client.executor, "GetKafkaCluster", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.GetKafkaCluster(ctx, name, params)
		})
	if err != nil {
		return KafkaCluster{}, err
	}
	return kafkaClusterFrom(response), nil
}

// List enumerates every Kafka cluster. The list response carries no subscriptions.
func (k *KafkaClusters) List(ctx context.Context) ([]KafkaCluster, error) {
	response, err := api.Execute[generated.ListKafkaClustersResponse](ctx, k.client.executor, "ListKafkaClusters", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.ListKafkaClusters(ctx)
		})
	if err != nil {
		return nil, err
	}

	clusters := make([]KafkaCluster, 0, len(response.Clusters))
	for _, cluster := range response.Clusters {
		clusters = append(clusters, KafkaCluster{
			Name:       cluster.Name,
			Properties: cluster.Properties,
			CreatedAt:  cluster.CreatedAt,
			Info:       noticesFrom(cluster.Info),
		})
	}
	return clusters, nil
}

// Update replaces a cluster's properties.
//
// The identity is the name, which is why Update takes a name and cannot change it: the API addresses
// the update by name, so a rename is a different cluster.
//
// Retry safety is RetryServerErrors: the request replaces the whole property map with an absolute
// value, so repeating it converges.
func (k *KafkaClusters) Update(ctx context.Context, name string, properties map[string]string) (KafkaCluster, error) {
	if name == "" {
		return KafkaCluster{}, errors.New("a Kafka cluster name is required")
	}
	if err := ValidateKafkaProperties(properties); err != nil {
		return KafkaCluster{}, err
	}

	copied := make(map[string]string, len(properties))
	for property, value := range properties {
		copied[property] = value
	}

	response, err := api.Execute[generated.KafkaClusterResponse](ctx, k.client.executor, "UpdateKafkaCluster", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.UpdateKafkaCluster(ctx, name, generated.UpdateKafkaClusterJSONRequestBody{Properties: copied})
		})
	if err != nil {
		return KafkaCluster{}, err
	}
	return kafkaClusterFrom(response), nil
}

// Delete removes a Kafka cluster.
//
// The API's `force` query parameter is sent only when the caller explicitly asks for it: forced
// deletion removes a cluster that subscriptions still reference, orphaning them. The default leaves
// the API's own dependency check in charge.
func (k *KafkaClusters) Delete(ctx context.Context, name string, options DeleteKafkaClusterOptions) error {
	if name == "" {
		return errors.New("a Kafka cluster name is required")
	}

	params := &generated.DeleteKafkaClusterParams{}
	if options.Force {
		force := true
		params.Force = &force
	}

	err := api.ExecuteNoContent(ctx, k.client.executor, "DeleteKafkaCluster", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.DeleteKafkaCluster(ctx, name, params)
		})
	if api.IsNotFound(err) {
		return nil
	}
	if err != nil && api.IsConflict(err) && !options.Force {
		return fmt.Errorf("Kafka cluster %s still has subscriptions referencing it; delete those subscriptions first, or opt into forced deletion, which orphans them: %w", name, err)
	}
	return err
}

func kafkaClusterFrom(response generated.KafkaClusterResponse) KafkaCluster {
	cluster := KafkaCluster{
		Name:       response.Name,
		Properties: response.Properties,
		CreatedAt:  response.CreatedAt,
		Info:       noticesFrom(response.Info),
	}
	for _, subscription := range response.Subscriptions {
		cluster.Subscriptions = append(cluster.Subscriptions, subscriptionFrom(subscription))
	}
	return cluster
}
