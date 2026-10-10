---
id: gateway-config
title: Gateway Configuration
sidebar_position: 2
---

# Gateway Configuration

The `GatewayConfig` CRD provides gateway-scoped configuration for the AI Gateway external processor container. This allows you to configure environment variables and resource requirements at the Gateway level, rather than at the route level.

## Overview

Use `GatewayConfig` when you need to:

- Configure per-gateway OpenTelemetry tracing settings
- Set resource requirements (CPU/memory) for the external processor
- Share configuration across multiple Gateways
- Configure environment variables for different gateway instances without affecting others

## Usage

### Creating a GatewayConfig

Create a `GatewayConfig` resource with your desired configuration:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: my-gateway-config
  namespace: default
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector:4317"
        - name: OTEL_SERVICE_NAME
          value: "my-ai-gateway"
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
        limits:
          cpu: "500m"
          memory: "512Mi"
```

### Referencing from a Gateway

Reference the `GatewayConfig` from your Gateway using an annotation:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-gateway
  namespace: default
  annotations:
    aigateway.envoyproxy.io/gateway-config: my-gateway-config
spec:
  gatewayClassName: envoy-gateway
  listeners:
    - name: http
      protocol: HTTP
      port: 8080
```

:::note
The `GatewayConfig` must be in the same namespace as the Gateway that references it.
:::

## Configuration Options

### Environment Variables

The `spec.extProc.kubernetes.env` field accepts a list of Kubernetes `EnvVar` objects:

```yaml
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "http://otel-collector:4317"
        - name: OTEL_EXPORTER_OTLP_HEADERS
          value: "api-key=your-secret"
        - name: LOG_LEVEL
          value: "debug"
```

### Resource Requirements

The `spec.extProc.kubernetes.resources` field configures compute resources for the external processor container:

```yaml
spec:
  extProc:
    kubernetes:
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
        limits:
          cpu: "500m"
          memory: "512Mi"
```

If not specified, Kubernetes default resource allocations are used.

## Embedding Response Validation and Trace Vectors

The AI Gateway Helm chart exposes two controller-wide options for OpenAI and Azure OpenAI embedding responses. These are chart values under `extProc`, not fields in the `GatewayConfig` CRD. Providers that require response-format translation retain their existing decoding.

| Helm value                         | Default | Behavior                                                                                                                                                                                                                  |
| ---------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `extProc.validateEmbeddingVectors` | `true`  | Preserve upstream full-response schema validation, including decoding vectors. Applies whether or not a trace is recorded.                                                                                                |
| `extProc.traceEmbeddingVectors`    | `true`  | When validation is `false`, capture valid vectors in recording spans on a best-effort basis. Set to `false` to skip vector decoding for tracing and retain model/token metadata. Has no effect when validation is `true`. |

To use lower-memory passthrough, including when tracing is enabled, configure the Helm values:

```yaml
extProc:
  validateEmbeddingVectors: false
  traceEmbeddingVectors: false
```

With validation disabled, the gateway still validates JSON syntax and model/usage metadata with case-sensitive field matching. It forwards the original response body unchanged and does not enforce vector types. If trace vector capture remains enabled, undecodable vectors are omitted from the trace without failing the request; invalid trace-only fields may cause trace data to be omitted. Changing trace sampling never changes the selected validation policy.

:::warning Memory cost

Validation defaults to `true` to preserve upstream behavior. It fully decodes embedding vectors and has substantially higher memory use. In a local 100 MiB response benchmark, decoding allocated approximately **2.66 GiB with validation enabled**, versus **256 MiB with it disabled and no vector capture**. The latter allocation level also held with a recording span when both options were `false`.

These are cumulative allocations per operation, not peak resident memory or recommended container limits. The protocol still buffers response bodies. Capturing vectors in traces also materializes them and can increase memory use even when validation is disabled. Size resources for the payload sizes and concurrency you expect.

:::

The chart passes these settings to controller flags `--extProcValidateEmbeddingVectors` and `--extProcTraceEmbeddingVectors`. The controller injects sidecar flags `-validateEmbeddingVectors` and `-traceEmbeddingVectors`; both CLI defaults are also `true`. Apply Helm value changes to the controller and roll gateway pods to receive the updated sidecar arguments.

See [Tracing](./observability/tracing.md#embedding-vector-capture) for how vector capture interacts with tracing configuration.

## Environment Variable Precedence

Environment variables can be configured at multiple levels. The precedence order is (highest to lowest):

1. **GatewayConfig.spec.extProc.kubernetes.env** - Highest priority
2. **Global controller flags** (`--extproc-extra-env-vars`) - Lower priority

When the same environment variable is defined at multiple levels, the higher precedence value is used.

### Example

If the controller is started with:

```
--extproc-extra-env-vars="LOG_LEVEL=info;GLOBAL_VAR=global"
```

And a GatewayConfig defines:

```yaml
env:
  - name: LOG_LEVEL
    value: "debug"
  - name: CONFIG_VAR
    value: "config"
```

The resulting environment variables will be:

- `LOG_LEVEL=debug` (GatewayConfig overrides global)
- `GLOBAL_VAR=global` (from global)
- `CONFIG_VAR=config` (from GatewayConfig)

## Shared Configuration

Multiple Gateways can reference the same `GatewayConfig`:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: shared-config
spec:
  extProc:
    kubernetes:
      env:
        - name: OTEL_SERVICE_NAME
          value: "ai-gateway-cluster"
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gateway-1
  annotations:
    aigateway.envoyproxy.io/gateway-config: shared-config
spec:
  # ...
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gateway-2
  annotations:
    aigateway.envoyproxy.io/gateway-config: shared-config
spec:
  # ...
```

## Migration from Route-Level Configuration

The route-level resource configuration (`AIGatewayRoute.spec.filterConfig.externalProcessor.resources`) is deprecated. Migrate to `GatewayConfig`:

### Before (deprecated)

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: my-route
spec:
  filterConfig:
    type: ExternalProcessor
    externalProcessor:
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
```

### After (recommended)

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: GatewayConfig
metadata:
  name: my-config
spec:
  extProc:
    kubernetes:
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-gateway
  annotations:
    aigateway.envoyproxy.io/gateway-config: my-config
spec:
  # ...
```

## Status

The `GatewayConfig` status reports the validity of the configuration:

```yaml
status:
  conditions:
    - type: Accepted
      status: "True"
      reason: Accepted
      message: "GatewayConfig reconciled successfully"
```

Possible condition types:

- `Accepted`: The configuration is valid and applied
- `NotAccepted`: The configuration has validation errors

## See Also

- [Tracing](./observability/tracing.md) - Configure distributed tracing for AI Gateway
- [Metrics](./observability/metrics.md) - Configure metrics collection
- [Examples](https://github.com/envoyproxy/ai-gateway/tree/main/examples/gateway-config) - Example YAML files
