# A key ID is only unique within an environment, so the import ID carries account, environment, and
# key. The bearer secret CANNOT be recovered by an import -- Restate Cloud returns it only when the
# key is created. After importing, `api_key` is null; the provider warns about this.
terraform import restate_cloud_api_key.admin acc_1a2b3c/env_1a2b3c/key_1a2b3c
