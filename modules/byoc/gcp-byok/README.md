# Google Cloud, bring your own Kubernetes

This module builds the Google Cloud side of a BYOK region and registers it with Restate Cloud. In a BYOK region, you build the GKE cluster with your own Terraform, and Restate runs your environments in it. Restate does not need a grant in your project and holds no credential for your Google Cloud account. It does hold two credentials for the cluster. The region installation operator keeps a region access key in the cluster, and the control plane keeps the sealed token of the `restate-break-glass` ServiceAccount, which never expires until you delete that ServiceAccount or its Secret. Restate reaches the cluster through the Pinniped Concierge that the BYOC chart installs, and the API server needs no address that Restate can reach.

The module creates the cluster, its node pools, the identities and the snapshot bucket. It then asks Restate Cloud for a one-use registration key, stores the key as the Kubernetes Secret that the BYOC chart reads, and installs the chart. The chart's region installation operator presents the key to Restate Cloud, which creates the region, and then installs the releases that Restate Cloud chooses.

A BYOK region supports Google Cloud only. The module needs the `restatedev/restate` provider, configured with an account's Management API key, because it calls `restate_cloud_byok_registration_key`. The account must be enabled for BYOC.

## What it creates

| Resource | Purpose |
| --- | --- |
| A regional GKE cluster | Runs the chart and your environments. It uses workload identity, Dataplane V2 so that network policies are enforced, private nodes, shielded nodes and the Config Connector add-on. |
| `rbac_binding_config` on the cluster | Allows bindings to `system:unauthenticated` and refuses bindings to `system:authenticated`. The Concierge binds its login API to `system:unauthenticated`, because the impersonation proxy authorizes the anonymous token request with RBAC, and a hardened cluster refuses that binding. Nothing of Restate's needs the other group, and it would widen every signed-in identity. |
| The node pool `system` | Runs the chart's components, the cloud operator, the ingress and the tunnel. It has no taint. |
| The node pool `environments` | Runs your environments. Its nodes are tainted `restate.dev/environments=true:NoSchedule` and labelled `restate.dev/environments=true`, and the chart's `scheduling` lets environment pods use that label and that toleration and no others. |
| A network, a subnetwork, a router and Cloud NAT | Created when `network` is unset. The private nodes leave through Cloud NAT to pull Restate's images and reach the control plane. |
| The service account `<name>-nodes` | The nodes run as this account, instead of the project's default Compute Engine account, with the roles in `node_service_account_roles`. |
| The bucket `<name>-snapshots-<project>` | Holds the snapshots of environments. It uses uniform bucket-level access, refuses public access and refuses to be destroyed while it holds objects. |
| The service account `<name>-env` | The environments act as this account, which holds `roles/storage.objectAdmin` on the snapshot bucket and nothing else. |
| The service account `<name>-cnrm` | Config Connector acts as this account, which holds a role on `<name>-env` only, so it can change the IAM policy of that one account and of nothing else. |
| The Config Connector object | Points the add-on at `<name>-cnrm`. For each environment, restate-operator writes a Config Connector `IAMPolicyMember` that lets the environment's `restate` ServiceAccount act as `<name>-env`. Config Connector serves the `IAMPolicyMember` kind only after this object has been applied, so the first environment may wait for it. |
| `restate_cloud_byok_registration_key` | The one-use key and the id of the region it registers. |
| The namespace and the Secret `registration` | The Secret holds the key under `key`, in the namespace of the chart's release. |
| The chart's Helm release | Installs the region installation operator, the Concierge, the admission policies, restate-operator and the rest of the BYOC chart, with values taken from the resources above and from the variables. |
| Enabled APIs | Cloud Resource Manager, Compute Engine, Kubernetes Engine, IAM, IAM Service Account Credentials and Cloud Storage. They stay enabled after a destroy. |

## The order of an apply

The cluster and the node pools come first, and then the identities and the bucket. Config Connector's object follows after a wait, because the add-on creates that kind from a pod, which runs only once there is a node. The registration key is created after all of these, because it expires four hours after it is created and the chart has to present it before then. The Secret and the chart's release follow at once.

The apply does not wait for the chart's pods. The Twingate Connector's pod starts only after the installer has registered the region and written the Connector's token pair, so a wait would make the apply depend on the registration. A wait that fails would also leave a release that a later apply might uninstall and install again, and a new installation replaces the break-glass token with one that Restate never saw.

A destroy runs backwards. The chart is removed first, then the Secret, and then the registration key. With `deregister_region_on_destroy`, which is off by default, removing the key also removes the region at Restate Cloud, which revokes its access keys and removes the Restate side of its access to the cluster. Restate Cloud refuses while the region hosts environments, so the destroy stops there with the cluster still in place. Delete the environments and run the destroy again. With the default, the region stays registered after a destroy, and you remove it in the console or with `DeleteByokRegion`.

## Checking that the region came up

A successful apply means that the chart is installed, and says nothing about the registration. Look at the conditions of the `RegionInstallation` object that the chart creates:

```bash
kubectl get regioninstallations.cloud.restate.dev -o yaml
```

| Condition | Meaning |
| --- | --- |
| `Registered` | The installer presented the registration key and Restate Cloud accepted it. A refusal appears here with its reason. |
| `ConnectorTokens` | The installer received the Twingate Connector's token pair and wrote it, so the Connector can start. |
| `Activated` | The region reported its addresses, its bucket and its service account, and Restate Cloud has marked it active. |

If the first apply fails, or one of these conditions does not become true, inspect the cluster and the installer's logs before you apply again. Do not run another apply blindly. A second install of the chart creates a new break-glass token that Restate does not know, which turns off break-glass access until Restate is given the new one. A refused or expired key needs only a new registration key, as the next section describes.

## The registration key

Each key works once, for one account, one cloud and one provider region, and expires after four hours. Restate Cloud reserves the id of the region when it creates the key, and the module gives that id to the chart as `region.id`. The region itself exists only after the chart has registered.

Terraform keeps the key in its state, as it does for every secret. After the region has registered, or after the key has expired, the key in the state opens nothing.

If the key expired before the chart registered, create a new one:

```bash
terraform apply -replace=module.<name>.restate_cloud_byok_registration_key.this
```

The new key reserves a new region id, the Secret takes the new key and the chart is upgraded with the new id. Every key that no region has used counts against an account limit until it expires, and Restate Cloud answers 429 when the account holds too many.

Changing `region` or `restate_region_display_name` replaces the key, because the API takes both only when it creates a key and has no operation that changes either afterwards. With `deregister_region_on_destroy` on, the replacement also removes the region that the old key registered, and the plan says so in a warning.

## How the cluster is reached

Terraform installs the Secret and the chart through the cluster's API server, so the machine that runs Terraform needs a route to it. By default the API server has a public address, and any address may try to sign in. Set `master_authorized_networks` to the networks that run Terraform and your operators, because Restate needs no inbound access to the API server and a narrow list costs nothing. Set `enable_private_endpoint` to give it only an internal address, and run Terraform from inside the network.

Restate does not use the API server's address. It reaches the cluster through the Concierge, over a connection that the Twingate Connector in the cluster opens outwards, so the cluster needs outbound access to Restate Cloud and to the registry in `image_source_registry`, and no inbound rule for Restate.

## Images

Every Restate image in the cluster must come from `image_source_registry`. Mirror Restate's images there before the apply, and make sure the nodes can pull from it. For an Artifact Registry in the region's project, add `roles/artifactregistry.reader` to `node_service_account_roles`. The chart refuses a restate-operator image that is not `<image_source_registry>/restate-operator` and a canary image that is not `<image_source_registry>/alpine:<tag>`, and the module sets both.

## Inputs

| Variable | Description |
| --- | --- |
| `project` | The project the cluster, the bucket and the service accounts are created in. Use a project of its own for the region. |
| `region` | The Google Cloud region, for example `us-central1`. The cluster is regional, and Restate registers the region under this name. |
| `control_plane_url` | The `https://api.<domain>` address of Restate's control plane, which Restate Cloud provides. |
| `image_source_registry` | The registry and path that every Restate image comes from, with no trailing slash. |
| `byoc_chart` | Where the BYOC chart comes from: `chart` and optionally `repository`, `version` and `dependency_update`. For a chart directory, set `chart` to its path and `dependency_update` to true. |
| `name` | The name of the cluster and the prefix of the service accounts and the bucket. The default is `restate`. It must differ between regions in one project. |
| `labels` | Google Cloud labels for the cluster, its nodes and the bucket. |
| `restate_region_display_name` | The name the Restate Cloud console shows for the region. |
| `deregister_region_on_destroy` | Remove the region at Restate Cloud on a destroy. The default is false, which leaves the region registered. |
| `enable_apis` | Enable the Google Cloud APIs the module uses. The default is true. |
| `namespace`, `release_name`, `registration_secret_name` | The chart's namespace, the Helm release's name and the Secret's name. The defaults are `restate-system`, `byoc` and `registration`. |
| `staff_gate_enabled` | Let Restate staff reach the environment namespaces and nothing else in the cluster. The default is true. |
| `hold` | Keep the region at its applied release. The default is false. |
| `restate_operator_canary_image_tag` | The tag of `<image_source_registry>/alpine`. The default is `3.21`. |
| `chart_values` | More values for the chart as YAML documents, which Helm merges over the module's values. |
| `tunnel_zones` | The number of tunnel LoadBalancer Services, which the chart creates one for each zone, from 3 to 9. The cloud operator renders at least three tunnels and the installer accepts at most nine. The default is the length of `node_locations`, or else the number of available zones of the region capped at nine. The module refuses a default below three or a `node_locations` list longer than nine, so set `tunnel_zones` then. The installer waits until every Service has an address. |
| `helm_timeout_seconds` | How long Terraform waits for Config Connector's object. The BYOC chart's release is not waited for. The default is 900. |
| `release_channel`, `kubernetes_version` | The GKE release channel, `REGULAR` by default, and an optional minimum version. The chart needs Kubernetes 1.30 or later. |
| `deletion_protection` | GKE's protection against deleting the cluster. The default is true. |
| `network`, `subnet_cidr`, `pods_cidr`, `services_cidr` | A subnetwork of your own with its secondary range names, or the ranges of the network the module creates. |
| `enable_private_nodes`, `enable_private_endpoint`, `master_ipv4_cidr_block`, `master_authorized_networks` | How the cluster is reached. The nodes are private by default. |
| `node_locations` | The zones of the node pools. Give at least three, because the chart needs three tunnel Services, or set `tunnel_zones` when you give fewer. |
| `system_node_pool`, `environments_node_pool` | The machine type, the node counts per zone and the disk of each pool. |
| `node_service_account_roles` | The project roles of the nodes' service account. |
| `snapshot_bucket_name`, `snapshot_bucket_force_destroy` | The bucket's name and whether a destroy deletes the snapshots in it. |
| `config_connector_environment_role` | The role Config Connector's account holds on `<name>-env`. The default is `roles/iam.serviceAccountAdmin`, which is what Managed BYOC regions grant. |
| `config_connector_ready_delay` | How long to wait before the Config Connector object is applied. The default is `120s`. |

## Outputs

| Output | Description |
| --- | --- |
| `region_id` | The id of the region. Use it as the `region` of `restate_cloud_environment` once the region is active. |
| `registration_key_expires_at` | When the key stops working if the chart has not registered. |
| `cluster_name`, `cluster_location`, `cluster_endpoint` | The cluster. |
| `namespace` | The namespace of the chart's release. |
| `snapshot_bucket`, `environments_service_account` | The values of the chart's `environments.snapshotBucket` and `environments.gcpServiceAccount`. |
| `config_connector_service_account`, `node_service_account` | The other two service accounts. |
| `scheduling` | The node selector and tolerations that the chart lets environment pods use. |

## Limits of this module

The module was tested with mocked providers only, and no cluster has been built with it. These parts depend on details that the tests cannot check, and each is behind a variable or a clearly named resource so that a first real run can change it.

| Part | What to check |
| --- | --- |
| The wait before Config Connector's object | The add-on creates the `ConfigConnector` kind from a pod, so the kind appears some time after the first node. If the first apply fails with a missing kind, raise `config_connector_ready_delay` and apply again. |
| The role on `<name>-env` | `roles/iam.serviceAccountAdmin` on that one account is what Managed BYOC grants. A narrower custom role needs a test that Config Connector's `IAMPolicyMember` still works with it. |
| The chart's values | The module sets the values listed in the chart's README and `restateOperator.gcpWorkloadIdentity`, which the chart passes to restate-operator. A chart release that renames one of them needs a matching release of this module. |
| Environment scheduling | The chart limits which node selector and tolerations environment pods may use. Whether Restate's control plane sets them on a BYOK environment is a question for the control plane's release, and until it does, environment pods that name neither stay off the tainted pool. |
| Static addresses | The chart creates the ingress and tunnel Services, and this module reserves no addresses for them. |
| The number of tunnel Services | The module assumes that a regional cluster uses every available zone of its region, or the zones in `node_locations`. If the region reports another number of zones, set `tunnel_zones`. The chart accepts 3 to 9 tunnel Services, so a region with fewer than three available zones, or a node pool in fewer than three zones, needs `tunnel_zones` set to at least 3, and the plan refuses it otherwise. |
