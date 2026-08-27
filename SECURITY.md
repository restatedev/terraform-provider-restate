# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem. Email **security@restate.dev** with a
description, affected versions, and reproduction steps. We will acknowledge receipt and keep you
informed while we investigate.

## Secrets and Terraform state

This provider handles bearer credentials for two separate APIs.

- The account-scoped management API key (`rst:role::ManagementApiAccess`) is provider
  configuration. It is sent only to the Cloud Management API and is never written to resource
  state.
- Environment-scoped API keys are sent only to the configured environment Admin API.
- `restate_cloud_api_key` receives its secret from the create response only. The secret is stored
  in Terraform state, marked sensitive, and cannot be recovered by a later read or import.
- `restate_kafka_cluster` properties can contain SASL passwords. The Admin API redacts them on
  read, so the provider preserves the configured values in state.

Terraform state is therefore sensitive regardless of CLI output redaction. Use an encrypted,
access-controlled state backend. Never commit state or `.tfvars` containing credentials.

## Logging

The provider redacts `Authorization` headers, deployment `additional_headers`, and Kafka
properties from diagnostics and from `TF_LOG` output. If you find a credential in provider output,
treat it as a vulnerability and report it.
