# NIM Backend Metrics Mock Server

This directory contains a mock NIM (NVIDIA Inference Microservices) backend server for testing the frontend's on-demand metrics collection feature.

## Purpose

**NOTE: This is temporary code.** Once NIM starts using Dynamo backend components natively, this mock server and the associated NIM metrics polling code will be removed.

This example demonstrates:
- How the Dynamo frontend can poll external backends for metrics
- Dynamic metric generation and collection
- The `runtime_stats` endpoint pattern
- Integration between frontend metrics and backend services

## Running the Example

### 1. Start the Mock NIM Backend

```bash
python3 examples/custom_backend/nim/mock_nim_server.py
```

This starts a backend on `dynamo/backend/runtime_stats` that returns incrementing metrics.

### 2. Start the Frontend with On-Demand Metrics

```bash
NIM_METRICS_ON_DEMAND=1 python3 -m dynamo.frontend \
    --model-name test-model \
    --model-path /path/to/model \
    --engine-type static
```

### 3. Query Metrics

```bash
curl http://localhost:8000/metrics
```

Each time you hit the `/metrics` endpoint, the frontend will:
1. Poll the mock NIM backend via the `runtime_stats` endpoint
2. Parse the returned metrics
3. Update Prometheus gauges dynamically
4. Include them in the metrics response

## Metrics Exposed

The mock server returns:

**Gauges:**
- `kv_cache_usage_perc` - Cycles between 0.30 and 0.93
- `gpu_utilization_perc` - Cycles between 50 and 97.5
- `active_requests` - Cycles 0-14
- `memory_used_gb` - Random between 12.5 and 14.5

**Counters:**
- `total_requests` - Increments with each request
- `total_tokens_generated` - `request_count * 127`

## Implementation Details

The frontend's NIM metrics collection is implemented in:
- `lib/llm/src/http/service/nim.rs` - NIM-specific metrics collection (temporary)
- `lib/llm/src/http/service/metrics.rs` - Metrics router with NIM support
- `components/src/dynamo/frontend/main.py` - `NIM_METRICS_ON_DEMAND` flag

All NIM-specific code is marked with TODO comments for removal once NIM adopts Dynamo backend.
