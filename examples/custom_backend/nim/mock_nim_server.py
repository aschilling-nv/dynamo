#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""
Mock NIM Backend Server for Metrics Testing

This server mocks a NIM (NVIDIA Inference Microservices) backend that exposes
runtime statistics via the runtime_stats endpoint.

NOTE: This is temporary code for testing purposes only. Once NIM starts using
Dynamo backend components natively, this mock server and the associated NIM
metrics polling code in the frontend will be removed. The NIM-specific metrics
collection exists only as a bridge until NIM adopts the Dynamo runtime.

The server demonstrates:
- Dynamic metric generation (gauges and counters)
- Proper async generator pattern for Dynamo endpoints
- JSON-encoded metric responses compatible with the frontend metrics collector
"""
import asyncio
import random
import time

import msgspec
import uvloop

from dynamo.runtime import DistributedRuntime, dynamo_worker

# Global counter for incrementing metrics
request_count = 0


async def handle_stats_request(request_bytes: str):
    """Mock stats handler - returns incrementing metrics for testing"""
    global request_count
    request_count += 1

    print(
        f"Received stats request #{request_count}: {request_bytes[:50] if request_bytes else '(empty)'}"
    )

    # Simulate changing metrics
    kv_cache_usage = 0.3 + (request_count % 10) * 0.07  # Cycles between 0.3 and 0.93
    gpu_utilization = 50 + (request_count % 20) * 2.5  # Cycles between 50 and 97.5
    active_requests = request_count % 15  # Cycles 0-14

    stats = {
        "schema_version": 1,
        "worker_id": "mock-worker-1",
        "backend": "vllm",
        "ts": int(time.time()),
        "metrics": {
            "gauges": {
                "kv_cache_usage_perc": round(kv_cache_usage, 2),
                "gpu_utilization_perc": round(gpu_utilization, 1),
                "active_requests": active_requests,
                "memory_used_gb": round(12.5 + random.random() * 2, 2),
            },
            "counters": {
                "total_requests": request_count,
                "total_tokens_generated": request_count * 127,
            },
        },
    }
    yield msgspec.json.encode(stats).decode("utf-8")


@dynamo_worker(static=False)
async def worker(runtime: DistributedRuntime):
    component = runtime.namespace("dynamo").component("backend")
    await component.create_service()

    stats_endpoint = component.endpoint("runtime_stats")
    print("Mock NIM stats server started on dynamo/backend/runtime_stats endpoint")
    print(
        "Exposing incrementing metrics: kv_cache_usage_perc, gpu_utilization_perc, active_requests, memory_used_gb, counters"
    )

    await stats_endpoint.serve_endpoint(handle_stats_request)


if __name__ == "__main__":
    uvloop.install()
    asyncio.run(worker())
