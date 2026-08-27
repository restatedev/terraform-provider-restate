package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// APIKeys is the endpoint adapter for environment-scoped API keys.
type APIKeys struct{ client *Client }

// APIKeys returns the API key adapter for this client's account.
func (c *Client) APIKeys() *APIKeys { return &APIKeys{client: c} }

// API keys support create, read and delete. There is deliberately no Updater: the contract has no
// update operation, so every change — including to a key's description — must replace the key. An
// Update method that quietly deleted and recreated a key would silently invalidate whatever is
// authenticating with it.
var (
	_ api.Creator[CreateAPIKeyInput, APIKey] = (*APIKeys)(nil)
	_ api.Getter[APIKeyRef, APIKey]          = (*APIKeys)(nil)
	_ api.Deleter[APIKeyRef, struct{}]       = (*APIKeys)(nil)
)

// Create mints an environment-scoped API key and returns it including its secret.
//
// The secret is available from this response and from nowhere else. A caller that loses it must
// delete the key and create another.
//
// Retry safety is RetryThrottleOnly: a 5xx from a create may mean the key exists, and a retry would
// mint a second credential that nothing tracks.
func (k *APIKeys) Create(ctx context.Context, input CreateAPIKeyInput) (APIKey, error) {
	if input.EnvironmentID == "" {
		return APIKey{}, errors.New("an environment ID is required")
	}
	// Validate rather than trusting the caller: this is the boundary that must make it impossible
	// to mint an account-scoped management key through the environment key path.
	if _, err := ParseEnvironmentRole(input.Role.String()); err != nil {
		return APIKey{}, err
	}

	body := generated.CreateApiKeyJSONRequestBody{
		EnvironmentId: input.EnvironmentID,
		RoleId:        generated.RoleId(input.Role),
	}
	if input.Description != "" {
		body.Description = &input.Description
	}

	response, err := api.Execute[generated.CreateApiKeyResponse](ctx, k.client.executor, "CreateApiKey", api.RetryThrottleOnly,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.CreateApiKey(ctx, k.client.accountID, body)
		})
	if err != nil {
		return APIKey{}, err
	}
	if response.KeyId == "" {
		return APIKey{}, &api.Error{
			Service:   api.ServiceCloud,
			Operation: "CreateApiKey",
			Message:   "the API reported success but returned no key ID",
		}
	}
	if response.ApiKey == "" {
		// Without the secret the key is useless and unrecoverable, so this is a failure even though
		// the key now exists. Name the key ID so it can be cleaned up.
		return APIKey{ID: response.KeyId, EnvironmentID: response.EnvironmentId}, &api.Error{
			Service:   api.ServiceCloud,
			Operation: "CreateApiKey",
			Message: fmt.Sprintf("the API created key %s but returned no secret, which cannot be recovered by a later read; "+
				"delete the key and try again", response.KeyId),
		}
	}

	return APIKey{
		ID:            response.KeyId,
		EnvironmentID: response.EnvironmentId,
		AccountID:     response.AccountId,
		Role:          Role(response.RoleId),
		State:         KeyState(response.State),
		Description:   input.Description,
		Secret:        response.ApiKey,
	}, nil
}

// Get describes one API key. The returned APIKey has an empty Secret: the API does not return the
// bearer token after create, so a caller refreshing state must keep whatever secret it already has
// rather than overwriting it with this zero value.
//
// A key the API reports as DELETED is returned as-is; see GetActive.
func (k *APIKeys) Get(ctx context.Context, ref APIKeyRef) (APIKey, error) {
	if ref.EnvironmentID == "" {
		return APIKey{}, errors.New("an environment ID is required")
	}
	if ref.KeyID == "" {
		return APIKey{}, errors.New("a key ID is required")
	}

	response, err := api.Execute[generated.DescribeApiKeyResponse](ctx, k.client.executor, "DescribeApiKey", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.DescribeApiKey(ctx, k.client.accountID,
				generated.DescribeApiKeyJSONRequestBody{EnvironmentId: ref.EnvironmentID, KeyId: ref.KeyID})
		})
	if err != nil {
		return APIKey{}, err
	}

	return APIKey{
		ID:            response.KeyId,
		EnvironmentID: response.EnvironmentId,
		AccountID:     response.AccountId,
		Role:          Role(response.RoleId),
		State:         KeyState(response.State),
		Description:   derefString(response.Description),
	}, nil
}

// ErrAPIKeyGone reports that an API key no longer exists, whether because the API returned
// not-found or because it reported the key as DELETED.
var ErrAPIKeyGone = errors.New("the API key no longer exists")

// GetActive describes a key and collapses both forms of absence into ErrAPIKeyGone.
func (k *APIKeys) GetActive(ctx context.Context, ref APIKeyRef) (APIKey, error) {
	key, err := k.Get(ctx, ref)
	if err != nil {
		if api.IsNotFound(err) {
			return APIKey{}, fmt.Errorf("%w: %s", ErrAPIKeyGone, ref)
		}
		return APIKey{}, err
	}
	if key.State == KeyStateDeleted {
		return key, fmt.Errorf("%w: %s reports state %s", ErrAPIKeyGone, ref, key.State)
	}
	return key, nil
}

// ListForEnvironment enumerates the key IDs in one environment.
//
// The contract's list response carries only identifiers — no role, no state, no description — so
// this returns references rather than pretending to return keys. Callers that need a key's
// attributes must describe it.
//
// This is not the generic Lister capability: keys are scoped to an environment, and there is no
// account-wide environment key listing to implement Lister against.
func (k *APIKeys) ListForEnvironment(ctx context.Context, environmentID string) ([]APIKeyRef, error) {
	if environmentID == "" {
		return nil, errors.New("an environment ID is required")
	}

	response, err := api.Execute[generated.ListApiKeysResponse](ctx, k.client.executor, "ListApiKeys", api.RetryTransportErrors,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.ListApiKeys(ctx, k.client.accountID,
				generated.ListApiKeysJSONRequestBody{EnvironmentId: environmentID})
		})
	if err != nil {
		return nil, err
	}

	refs := make([]APIKeyRef, 0, len(response.ApiKeys))
	for _, key := range response.ApiKeys {
		refs = append(refs, APIKeyRef{EnvironmentID: key.EnvironmentId, KeyID: key.KeyId})
	}
	return refs, nil
}

// Delete revokes an API key. It is idempotent: an already-absent key is success.
//
// The Options type is struct{} because the contract's delete takes no switches. Deleting a key
// immediately invalidates whatever is authenticating with it, but that is the operation's purpose
// rather than a destructive side effect, so it needs no separate opt-in.
func (k *APIKeys) Delete(ctx context.Context, ref APIKeyRef, _ struct{}) error {
	if ref.EnvironmentID == "" {
		return errors.New("an environment ID is required")
	}
	if ref.KeyID == "" {
		return errors.New("a key ID is required")
	}

	err := api.ExecuteNoContent(ctx, k.client.executor, "DeleteApiKey", api.RetryServerErrors,
		func(ctx context.Context) (*http.Response, error) {
			return k.client.generated.DeleteApiKey(ctx, k.client.accountID,
				generated.DeleteApiKeyJSONRequestBody{EnvironmentId: ref.EnvironmentID, KeyId: ref.KeyID})
		})
	if api.IsNotFound(err) {
		return nil
	}
	return err
}
