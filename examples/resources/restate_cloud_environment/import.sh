# Environments are imported by account ID and environment ID together. The account is part of the
# identity so importing into a provider configured for a different account is refused rather than
# silently adopting an environment from elsewhere.
terraform import restate_cloud_environment.production acc_1a2b3c/env_1a2b3c
