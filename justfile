# Development tasks for the Restate Terraform provider. Run `just` to list them.
#
# The provider is a plugin, not a standalone tool, so a build is only useful once a CLI can find
# it. `just install` writes it into the user plugin directory that `terraform init` and `tofu init`
# search, which is what lets a configuration use this working tree before the provider is published
# to either registry.

binary := "terraform-provider-restate"
source := "restatedev/restate"

# The version a local build installs as.
#
# It must not be a prerelease such as 0.0.0-dev: both CLIs skip prerelease versions when resolving
# a `required_providers` entry that does not pin one exactly, so the build would install and then
# never be selected. Keep it above any released version you have also installed here -- init picks
# the highest it can see.
version := "0.0.1"

goos := `go env GOOS`
goarch := `go env GOARCH`
commit := `git rev-parse --short HEAD 2>/dev/null || echo unknown`

# Terraform and OpenTofu read the same user plugin directory, and differ only in the registry host
# they resolve `restatedev/restate` to, so each needs its own tree underneath it. Nothing but this
# justfile writes here -- a provider downloaded from a registry is cached elsewhere -- so `uninstall`
# cannot delete a real install.
plugin_root := home_directory() / ".terraform.d" / "plugins"

_default:
    @just --list --unsorted

# Compile the provider into dist/.
build:
    mkdir -p dist
    go build -ldflags "-X main.version={{ version }} -X main.commit={{ commit }}" -o dist/{{ binary }} .

# Run the unit and protocol tests, as CI does. Acceptance tests are excluded; see `test-acc`.
test:
    go test -race -count=1 -timeout=20m ./...

# Run the acceptance tests against a real account, creating and destroying live resources.
test-acc:
    TF_ACC=1 go test ./internal/provider/ -run TestAcc -v -timeout 90m

# Install a local build for both Terraform and OpenTofu.
install: (_install "registry.terraform.io") (_install "registry.opentofu.org")
    @echo "installed {{ binary }} {{ version }} for {{ goos }}_{{ goarch }}"
    @echo "a configuration requiring {{ source }} now resolves to this build on init"

# Install a local build for Terraform only.
install-terraform: (_install "registry.terraform.io")

# Install a local build for OpenTofu only.
install-tofu: (_install "registry.opentofu.org")

# Remove every locally installed build, for both CLIs.
uninstall:
    rm -rf "{{ plugin_root }}/registry.terraform.io/{{ source }}"
    rm -rf "{{ plugin_root }}/registry.opentofu.org/{{ source }}"

_install registry: build
    mkdir -p "{{ plugin_root }}/{{ registry }}/{{ source }}/{{ version }}/{{ goos }}_{{ goarch }}"
    cp dist/{{ binary }} "{{ plugin_root }}/{{ registry }}/{{ source }}/{{ version }}/{{ goos }}_{{ goarch }}/{{ binary }}_v{{ version }}"
