# SCC Operator

[![Go Report Card](https://goreportcard.com/badge/github.com/rancher/scc-operator)](https://goreportcard.com/report/github.com/rancher/scc-operator)

The SCC (SUSE Customer Center) Operator is a Kubernetes operator that manages product registration and telemetry for SUSE Linux Enterprise (SLE) based products running in Kubernetes clusters. It is specifically designed to integrate with Rancher and enable automated subscription management and telemetry reporting.

## Table of Contents

- [Overview](#overview)
- [What is SUSE Customer Center (SCC)?](#what-is-suse-customer-center-scc)
- [Architecture](#architecture)
- [Integration with Rancher](#integration-with-rancher)
- [Features](#features)
- [Installation](#installation)
- [Configuration](#configuration)
- [Usage](#usage)
- [API Reference](#api-reference)
- [Development](#development)
- [Building](#building)
- [Contributing](#contributing)
- [License](#license)

## Overview

The SCC Operator automates the process of registering Kubernetes clusters with SUSE Customer Center, enabling:

- Automated product registration and activation
- Subscription management and validation
- Telemetry data collection and reporting
- Support for both online and offline registration modes
- Seamless integration with Rancher's management platform

## What is SUSE Customer Center (SCC)?

SUSE Customer Center (SCC) is SUSE's central platform for managing product subscriptions, updates, and support entitlements. It provides:

- **Product Registration**: Register SUSE products to receive updates and support
- **Subscription Management**: Track and manage product subscriptions and entitlements
- **Update Delivery**: Access to product updates, patches, and security fixes
- **Telemetry**: Collect usage data to improve products and support

The SCC Operator bridges the gap between Kubernetes-based deployments and SUSE's subscription management infrastructure.

## Architecture

The SCC Operator is built using the following components:

### Core Components

```
┌─────────────────────────────────────────────────────────────┐
│                      SCC Operator                            │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  ┌──────────────┐  ┌─────────────────┐  ┌───────────────┐  │
│  │ Registration │  │   Credentials   │  │   Telemetry   │  │
│  │  Controller  │  │   Management    │  │   Reporting   │  │
│  └──────────────┘  └─────────────────┘  └───────────────┘  │
│         │                   │                     │         │
│         └───────────────────┴─────────────────────┘         │
│                            │                                │
│                 ┌──────────┴──────────┐                     │
│                 │   SUSE Connect NG   │                     │
│                 │      Wrapper        │                     │
│                 └─────────────────────┘                     │
└─────────────────────────────────────────────────────────────┘
                            │
                            ▼
            ┌───────────────────────────┐
            │  SUSE Customer Center API  │
            └───────────────────────────┘
```

### Key Controllers

1. **Registration Controller** (`pkg/controllers/online.go`, `pkg/controllers/offline.go`)
   - Manages product registration lifecycle
   - Handles both online and offline registration modes
   - Synchronizes registration status with SCC

2. **Credentials Management** (`internal/suseconnect/credentials/`)
   - Securely manages registration credentials
   - Handles registration codes and certificates
   - Manages system credentials for authenticated API access

3. **Telemetry Module** (`internal/telemetry/`)
   - Collects and reports usage metrics
   - Integrates with Rancher's telemetry system
   - Manages SecretRequest resources for metric collection

4. **Offline Support** (`internal/suseconnect/offline/`)
   - Enables registration in air-gapped environments
   - Generates offline registration requests
   - Processes offline activation certificates

## Integration with Rancher

The SCC Operator is designed to work seamlessly within Rancher-managed Kubernetes clusters:

### How It Integrates

1. **Deployment**
   - Deployed as part of Rancher's system components
   - Runs in the `cattle-scc-system` namespace (by default)
   - Managed through Rancher's operator lifecycle

2. **Configuration**
   - Reads Rancher settings (`management.cattle.io/v3/settings`):
     - `install-uuid`: Unique identifier for the Rancher installation
     - `server-url`: Rancher server URL
     - `server-version`: Rancher version information
   - Uses these settings to populate telemetry data

3. **Telemetry Integration**
   - Creates `SecretRequest` resources (`telemetry.cattle.io/v1`)
   - Rancher's telemetry operator processes these requests
   - Metrics data flows through Rancher's telemetry pipeline

4. **Resource Management**
   - Uses Rancher's Wrangler framework for controller management
   - Integrates with Rancher's RBAC and authentication
   - Follows Rancher's patterns for resource labeling and ownership

### Registration Flow in Rancher

```
┌──────────────┐
│   Rancher    │
│  Dashboard   │
└──────┬───────┘
       │ 1. Create Registration CR
       ▼
┌──────────────────────────────────────┐
│       SCC Operator                    │
│  ┌────────────────────────────────┐  │
│  │  Registration Controller       │  │
│  └────────┬───────────────────────┘  │
│           │ 2. Process Registration  │
│           ▼                          │
│  ┌────────────────────────────────┐  │
│  │  SUSE Connect Wrapper          │  │
│  └────────┬───────────────────────┘  │
└───────────┼──────────────────────────┘
            │ 3. Call SCC API
            ▼
┌─────────────────────────┐
│  SUSE Customer Center   │
│  - Validate credentials │
│  - Activate product     │
│  - Return system ID     │
└─────────┬───────────────┘
          │ 4. Update status
          ▼
┌─────────────────────────┐
│  Registration CR Status │
│  - systemID: 12345      │
│  - activated: true      │
│  - expiresAt: ...       │
└─────────────────────────┘
```

### Rancher-Specific Features

- **DevMode Support**: Special development mode for testing (`cattle-dev-mode`)
- **Metrics Integration**: Automatic creation of `rancher-scc-metrics` secret
- **Namespace Convention**: Uses `cattle-` prefix for system namespaces
- **Label Standards**: Uses `scc.cattle.io` labels for resource tracking

## Features

### Registration Modes

1. **Online Registration**
   - Direct connection to SUSE Customer Center
   - Real-time activation and validation
   - Automatic keepalive and status updates

2. **Offline Registration**
   - Support for air-gapped environments
   - Generate registration requests locally
   - Import activation certificates manually

### Capabilities

- **Automated Registration**: Declare desired state, operator handles the rest
- **Credential Management**: Secure storage of registration codes and system credentials
- **Status Tracking**: Comprehensive status reporting with conditions
- **Lifecycle Management**: Handles registration, activation, renewal, and deactivation
- **Telemetry Reporting**: Automated collection and submission of usage metrics
- **Multi-Architecture**: Supports AMD64 and ARM64 platforms

## Installation

### Prerequisites

- Kubernetes cluster (1.24+)
- kubectl configured to access the cluster
- For Rancher integration: Rancher 2.7+ deployed

### Deploy the Operator

The SCC Operator is typically deployed as part of Rancher. For standalone deployment:

```bash
# Clone the repository
git clone https://github.com/rancher/scc-operator.git
cd scc-operator

# Build and push the image
make build-image TAG=v1.0.0

# Or push to a registry
make push-image TAG=v1.0.0 IMAGE_NAME=myregistry/scc-operator

# Deploy (assuming you have deployment manifests)
kubectl apply -f deploy/
```

### Configuration

The operator is configured via command-line flags:

```bash
scc-operator \
  --operator-name=rancher-scc-operator \
  --operator-namespace=cattle-scc-system \
  --lease-namespace=kube-system \
  --log-level=info \
  --log-format=json
```

## Configuration

### Operator Settings

The operator reads configuration from:

1. **Command-line flags** (highest priority)
2. **ConfigMap**: `scc-operator-config` in the operator namespace
3. **Environment variables** (via Rancher settings)

### Available Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--operator-name` | Name of the operator instance | `rancher-scc-operator` |
| `--operator-namespace` | Namespace where operator is deployed | `cattle-scc-system` |
| `--lease-namespace` | Namespace for leader election lease | `kube-system` |
| `--log-level` | Logging level (trace, debug, info, warn, error) | `info` |
| `--log-format` | Log format (json, text) | `text` |
| `--debug` | Enable debug logging | `false` |
| `--trace` | Enable trace logging | `false` |
| `--kubeconfig` | Path to kubeconfig (for out-of-cluster) | - |

## Usage

### Creating a Registration

#### Online Registration

Create a `Registration` resource with your registration code:

```yaml
apiVersion: scc.cattle.io/v1
kind: Registration
metadata:
  name: my-cluster-registration
spec:
  mode: online
  registrationRequest:
    registrationCodeSecretRef:
      name: scc-registration-code
      namespace: cattle-scc-system
---
apiVersion: v1
kind: Secret
metadata:
  name: scc-registration-code
  namespace: cattle-scc-system
type: Opaque
stringData:
  code: "YOUR-REGISTRATION-CODE"
```

Apply the resources:

```bash
kubectl apply -f registration.yaml
```

Check registration status:

```bash
kubectl get registration my-cluster-registration -o yaml
```

#### Offline Registration

For air-gapped environments:

1. Create an offline registration request:

```yaml
apiVersion: scc.cattle.io/v1
kind: Registration
metadata:
  name: my-cluster-registration
spec:
  mode: offline
  registrationRequest:
    registrationCodeSecretRef:
      name: scc-registration-code
      namespace: cattle-scc-system
```

2. The operator generates an offline request in a secret

3. Extract the request and submit to SCC manually

4. Import the activation certificate:

```yaml
spec:
  offlineRegistrationCertificateSecretRef:
    name: scc-activation-cert
    namespace: cattle-scc-system
```

### Monitoring Registration Status

Check the status of a registration:

```bash
# List all registrations
kubectl get registrations

# Get detailed status
kubectl describe registration my-cluster-registration

# Watch for status changes
kubectl get registration my-cluster-registration -w
```

The status provides:

- **Conditions**: Progress through registration lifecycle
- **System ID**: Unique identifier from SCC
- **Activation Status**: Whether registration is active
- **Expiration**: When registration expires
- **Last Sync**: Last successful sync with SCC

## API Reference

### Registration Custom Resource

The `Registration` CRD is the primary API for managing SCC registrations.

**API Group**: `scc.cattle.io/v1`

**Resource**: `registrations` (cluster-scoped)

#### Spec Fields

| Field | Type | Description |
|-------|------|-------------|
| `mode` | string | Registration mode: `online` or `offline` |
| `registrationRequest` | object | Registration request details |
| `registrationRequest.registrationCodeSecretRef` | object | Reference to secret containing registration code |
| `registrationRequest.registrationAPIUrl` | string | Optional custom SCC API URL |
| `registrationRequest.registrationAPICertificateSecretRef` | object | Optional custom CA certificate |
| `offlineRegistrationCertificateSecretRef` | object | Reference to offline activation certificate (offline mode only) |
| `syncNow` | bool | Trigger immediate sync with SCC |

#### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | array | Array of condition objects |
| `currentCondition` | object | Current primary condition |
| `sccSystemID` | int | System ID assigned by SCC |
| `registeredProduct` | string | Name of registered product |
| `registrationExpiresAt` | timestamp | Registration expiration time |
| `activationStatus` | object | Activation status details |
| `activationStatus.activated` | bool | Whether system is activated |
| `activationStatus.lastValidatedTS` | timestamp | Last successful validation |
| `systemCredentialsSecretRef` | object | Reference to system credentials secret |

#### Conditions

The operator reports status through conditions:

- `Done`: Registration completed successfully
- `Failure`: Registration failed
- `Progressing`: Registration in progress
- `Ready`: System ready for use
- `RegistrationAnnounced`: Initial registration request sent
- `RegistrationSccUrlReady`: SCC URL validated
- `RegistrationActivated`: Product activated with SCC
- `RegistrationKeepalive`: Keepalive connection maintained
- `OfflineRequestReady`: Offline request generated (offline mode)
- `OfflineCertificateReady`: Offline certificate processed (offline mode)
- `OfflineActivationDone`: Offline activation completed (offline mode)

## Development

### Prerequisites

- Go 1.24+
- Docker or Podman
- kubectl
- Access to a Kubernetes cluster for testing

### Setup Development Environment

```bash
# Clone the repository
git clone https://github.com/rancher/scc-operator.git
cd scc-operator

# Install dependencies
go mod download

# Generate code (controllers, CRDs, etc.)
go generate

# Run tests
make test

# Run linter
make lint
```

### Project Structure

```
scc-operator/
├── cmd/
│   └── operator/          # Main operator entry point
├── pkg/
│   ├── apis/              # API definitions (CRDs)
│   │   └── scc.cattle.io/v1/
│   ├── controllers/       # Main controllers
│   ├── crds/              # CRD management
│   ├── generated/         # Generated clientsets
│   └── operator/          # Operator setup and lifecycle
├── internal/
│   ├── consts/            # Constants and configuration
│   ├── suseconnect/       # SUSE Connect integration
│   │   ├── credentials/   # Credential management
│   │   └── offline/       # Offline registration support
│   ├── telemetry/         # Telemetry and metrics
│   ├── rancher/           # Rancher-specific integrations
│   └── initializer/       # Initialization and context
├── package/
│   └── Dockerfile         # Container image definition
├── scripts/               # Build and utility scripts
└── hack/                  # Development tools and makefiles
```

### Running Locally

```bash
# Build the binary
make build

# Run locally (requires kubeconfig)
./bin/scc-operator \
  --kubeconfig=$HOME/.kube/config \
  --operator-namespace=cattle-scc-system \
  --debug
```

### Testing

```bash
# Run unit tests
go test ./...

# Run specific package tests
go test ./pkg/controllers/...

# Run with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## Building

### Build Binary

```bash
# Build for current platform
make build

# Build with specific version
VERSION=v1.0.0 make build
```

### Build Container Image

```bash
# Build image for current platform
make build-image TAG=v1.0.0

# Build multi-arch image
TARGET_PLATFORMS=linux/amd64,linux/arm64 make build-image TAG=v1.0.0

# Push to registry
make push-image TAG=v1.0.0 REPO=myregistry IMAGE=scc-operator
```

### Release Process

The project uses GitHub Actions for automated releases:

1. Tag a release: `git tag -a v1.0.0 -m "Release v1.0.0"`
2. Push the tag: `git push origin v1.0.0`
3. GitHub Actions automatically:
   - Builds multi-arch images
   - Pushes to Docker Hub (`rancher/scc-operator`)
   - Creates GitHub release with artifacts

## Contributing

Contributions are welcome! Please follow these guidelines:

1. **Fork the repository** and create a feature branch
2. **Write tests** for new functionality
3. **Follow Go conventions** and existing code style
4. **Run linter** before committing: `make lint`
5. **Update documentation** as needed
6. **Submit a pull request** with a clear description

### Code Style

- Follow standard Go formatting (`gofmt`, `goimports`)
- Use meaningful variable and function names
- Add comments for exported functions and types
- Keep functions focused and concise

### Testing Standards

- Write unit tests for new code
- Maintain or improve code coverage
- Test both success and error paths
- Use table-driven tests where appropriate

## License

Copyright © 2024 SUSE LLC

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.

---

## Related Projects

- [Rancher](https://github.com/rancher/rancher) - Kubernetes management platform
- [SUSE Connect NG](https://github.com/SUSE/connect-ng) - SUSE registration client library
- [Wrangler](https://github.com/rancher/wrangler) - Kubernetes controller framework

## Support

For issues and questions:

- **GitHub Issues**: [github.com/rancher/scc-operator/issues](https://github.com/rancher/scc-operator/issues)
- **Rancher Slack**: Join the [Rancher Users Slack](https://slack.rancher.io/)
- **SUSE Support**: For enterprise support, contact [SUSE](https://www.suse.com/support/)
