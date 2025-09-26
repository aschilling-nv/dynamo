// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// TODO: This entire module is temporary and will be removed once NIM starts using Dynamo backend.
// NIM-specific metrics polling and collection logic.

use anyhow::Result;
use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

/// Dynamic NIM metrics registry that maintains gauges for metrics discovered at runtime
///
/// Note: These gauges are copies of metrics (gauges, counters, etc.) from the NIM backend.
/// They are implemented as gauges because they are synchronized/copied from upstream values
/// rather than being directly incremented or modified by this service.
///
/// DEPRECATION: Remove this once NIM uses Dynamo backend for metrics
pub struct NimMetricsRegistry {
    gauges: Mutex<HashMap<String, prometheus::Gauge>>,
    prefix: String,
}

impl NimMetricsRegistry {
    pub fn new(prefix: String) -> Self {
        Self {
            gauges: Mutex::new(HashMap::new()),
            prefix,
        }
    }

    /// Get or create a gauge for the given metric name
    fn get_or_create_gauge(&self, name: &str) -> prometheus::Gauge {
        let mut gauges = self.gauges.lock().unwrap();

        if let Some(gauge) = gauges.get(name) {
            return gauge.clone();
        }

        let full_name = if self.prefix.is_empty() {
            name.to_string()
        } else {
            format!("{}_{}", self.prefix, name)
        };

        let gauge =
            prometheus::Gauge::new(full_name.clone(), format!("NIM backend metric: {}", name))
                .unwrap_or_else(|e| {
                    tracing::error!("Failed to create gauge for {}: {}", name, e);
                    prometheus::Gauge::new("error_gauge", "Error gauge").unwrap()
                });

        gauges.insert(name.to_string(), gauge.clone());
        tracing::debug!("Created new NIM gauge metric: {}", full_name);
        gauge
    }

    /// Update a metric value by name
    pub fn set_metric(&self, name: &str, value: f64) {
        let gauge = self.get_or_create_gauge(name);
        gauge.set(value);
    }

    /// Get all gauges for registration
    pub fn get_all_gauges(&self) -> Vec<prometheus::Gauge> {
        let gauges = self.gauges.lock().unwrap();
        gauges.values().cloned().collect()
    }
}

/// Custom Prometheus collector that triggers NIM stats polling before metrics are gathered
///
/// DEPRECATION: Remove this once NIM uses Dynamo backend for metrics
pub(super) struct NimMetricsCollector {
    http_state: Arc<super::service_v2::State>,
    nim_registry: Arc<NimMetricsRegistry>,
}

impl NimMetricsCollector {
    fn new(
        http_state: Arc<super::service_v2::State>,
        nim_registry: Arc<NimMetricsRegistry>,
    ) -> Self {
        Self {
            http_state,
            nim_registry,
        }
    }
}

impl prometheus::core::Collector for NimMetricsCollector {
    fn desc(&self) -> Vec<&prometheus::core::Desc> {
        Vec::new()
    }

    fn collect(&self) -> Vec<prometheus::proto::MetricFamily> {
        let runtime = tokio::runtime::Handle::try_current();
        if let Ok(handle) = runtime {
            let http_state = self.http_state.clone();
            let nim_registry = self.nim_registry.clone();

            let result = tokio::task::block_in_place(|| {
                handle.block_on(async move {
                    super::service_v2::HttpService::poll_nim_backend_stats_with_registry(
                        &http_state,
                        &nim_registry,
                    )
                    .await
                })
            });

            if let Err(e) = result {
                tracing::error!("Failed to poll NIM backend stats in collector: {}", e);
            }
        } else {
            tracing::error!("No tokio runtime available for NIM metrics collector");
        }

        let mut metric_families = Vec::new();
        for gauge in self.nim_registry.get_all_gauges() {
            metric_families.extend(gauge.collect());
        }
        metric_families
    }
}

/// Register NIM metrics collector with the Prometheus registry if nim_on_demand is enabled
///
/// DEPRECATION: Remove this once NIM uses Dynamo backend for metrics
pub fn register_nim_collector_if_enabled(
    registry: &mut prometheus::Registry,
    nim_on_demand: bool,
    nim_state: Option<&Arc<super::service_v2::State>>,
) {
    if !nim_on_demand {
        return;
    }

    let Some(state) = nim_state else {
        return;
    };

    use dynamo_runtime::metrics::prometheus_names::{
        frontend_service, name_prefix, sanitize_frontend_prometheus_prefix,
    };

    let raw_prefix = std::env::var(frontend_service::METRICS_PREFIX_ENV)
        .unwrap_or_else(|_| name_prefix::FRONTEND.to_string());
    let prefix = sanitize_frontend_prometheus_prefix(&raw_prefix);
    let nim_registry = Arc::new(NimMetricsRegistry::new(prefix));
    let collector = NimMetricsCollector::new(state.clone(), nim_registry);

    if let Err(e) = registry.register(Box::new(collector)) {
        tracing::error!("Failed to register NIM metrics collector: {}", e);
    } else {
        tracing::info!("Registered NIM metrics collector for on-demand polling");
    }
}

/// Poll NIM backend stats from the runtime_stats endpoint
///
/// DEPRECATION: Remove this once NIM uses Dynamo backend for metrics
pub async fn poll_nim_backend_stats(_state: &Arc<super::service_v2::State>) -> Result<()> {
    // Hardcoded to match the NIM backend's namespace and component.
    // The backend registers itself as: runtime.namespace("dynamo").component("backend")
    // See: examples/custom_backend/nim/mock_nim_server.py
    let namespace = "dynamo";
    let component = "backend";

    if let Err(e) = get_nim_stats_from_endpoint(namespace, component, None).await {
        tracing::error!(
            namespace = %namespace,
            component = %component,
            error = %e,
            "Failed to poll NIM backend runtime_stats"
        );
    }

    Ok(())
}

/// Poll NIM backend stats and update the NimMetricsRegistry with dynamic metrics
///
/// DEPRECATION: Remove this once NIM uses Dynamo backend for metrics
pub async fn poll_nim_backend_stats_with_registry(
    _state: &Arc<super::service_v2::State>,
    nim_registry: &NimMetricsRegistry,
) -> Result<()> {
    // Hardcoded to match the NIM backend's namespace and component.
    // The backend registers itself as: runtime.namespace("dynamo").component("backend")
    // See: examples/custom_backend/nim/mock_nim_server.py
    let namespace = "dynamo";
    let component = "backend";

    if let Err(e) = get_nim_stats_from_endpoint(namespace, component, Some(nim_registry)).await {
        tracing::error!(
            namespace = %namespace,
            component = %component,
            error = %e,
            "Failed to poll NIM backend runtime_stats with registry"
        );
    }

    Ok(())
}

/// Get NIM stats from a specific component's runtime_stats endpoint
///
/// DEPRECATION: Remove this once NIM uses Dynamo backend for metrics
async fn get_nim_stats_from_endpoint(
    namespace: &str,
    component: &str,
    nim_registry: Option<&NimMetricsRegistry>,
) -> Result<()> {
    use dynamo_runtime::protocols::annotated::Annotated;
    use dynamo_runtime::{DistributedRuntime, pipeline::PushRouter};
    use futures::StreamExt;
    use std::time::Duration;

    // Create a temporary DistributedRuntime to access the component
    // TODO: This is not ideal - we should have access to the DRT from State
    let runtime = dynamo_runtime::Runtime::from_settings()?;
    let drt = DistributedRuntime::from_settings(runtime).await?;

    let component_obj = drt.namespace(namespace)?.component(component)?;

    // Try to get a client for the runtime_stats endpoint
    let endpoint = component_obj.endpoint("runtime_stats");

    let client = match endpoint.client().await {
        Ok(client) => client,
        Err(e) => {
            tracing::debug!(
                namespace = %namespace,
                component = %component,
                error = %e,
                "Failed to create runtime_stats client"
            );
            return Ok(());
        }
    };

    // Wait briefly for instances to be available
    if let Err(e) =
        tokio::time::timeout(Duration::from_millis(100), client.wait_for_instances()).await
    {
        tracing::debug!(
            namespace = %namespace,
            component = %component,
            "No runtime_stats instances available: {}",
            e
        );
        return Ok(());
    }

    // Create a PushRouter to call the runtime_stats endpoint
    let router =
        PushRouter::<String, Annotated<serde_json::Value>>::from_client(client, Default::default())
            .await?;

    // Call the runtime_stats endpoint with empty request (with timeout)
    let mut response_stream =
        match tokio::time::timeout(Duration::from_secs(2), router.random(String::new().into()))
            .await
        {
            Ok(Ok(stream)) => stream,
            Ok(Err(e)) => {
                tracing::debug!(
                    namespace = %namespace,
                    component = %component,
                    error = %e,
                    "Failed to call runtime_stats endpoint"
                );
                return Ok(());
            }
            Err(_) => {
                tracing::debug!(
                    namespace = %namespace,
                    component = %component,
                    "Timed out calling runtime_stats endpoint"
                );
                return Ok(());
            }
        };

    // Collect bounded responses with timeout per next()
    let mut responses = Vec::new();
    const MAX_ITEMS: usize = 128;
    while responses.len() < MAX_ITEMS {
        match tokio::time::timeout(Duration::from_millis(250), response_stream.next()).await {
            Ok(Some(response)) => responses.push(response),
            Ok(None) => break,
            Err(_) => {
                tracing::debug!(
                    namespace = %namespace,
                    component = %component,
                    "Timed out waiting for runtime_stats response item"
                );
                break;
            }
        }
    }

    tracing::debug!(
        namespace = %namespace,
        component = %component,
        response_count = responses.len(),
        "Successfully polled NIM runtime_stats endpoint"
    );

    // Parse the responses and update Prometheus metrics
    if let Some(registry) = nim_registry {
        for response in &responses {
            if let Some(data) = &response.data {
                // If data is a string, try to parse it as JSON
                let parsed_data = if let Some(json_str) = data.as_str() {
                    match serde_json::from_str::<serde_json::Value>(json_str) {
                        Ok(parsed) => Some(parsed),
                        Err(e) => {
                            tracing::error!("Failed to parse JSON string from NIM backend: {}", e);
                            None
                        }
                    }
                } else {
                    None
                };

                let data_to_process = parsed_data.as_ref().unwrap_or(data);

                if let Some(metrics_obj) = data_to_process.get("metrics")
                    && let Some(gauges) = metrics_obj.get("gauges")
                    && let Some(gauges_map) = gauges.as_object()
                {
                    for (metric_name, metric_value) in gauges_map {
                        if let Some(value) = metric_value.as_f64() {
                            tracing::debug!(
                                namespace = %namespace,
                                component = %component,
                                metric = %metric_name,
                                value = value,
                                "Updated NIM backend metric"
                            );
                            registry.set_metric(metric_name, value);
                        } else {
                            tracing::warn!(
                                namespace = %namespace,
                                component = %component,
                                metric = %metric_name,
                                "NIM metric value is not a valid f64"
                            );
                        }
                    }
                }
            }
        }
    }

    Ok(())
}
