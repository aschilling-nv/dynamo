# SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

from unittest.mock import AsyncMock, Mock

import pytest

from dynamo.planner.kubernetes_connector import KubernetesConnector


@pytest.fixture
def mock_kube_api():
    mock_api = Mock()
    mock_api.get_graph_deployment = AsyncMock()
    mock_api.update_graph_replicas = AsyncMock()
    mock_api.wait_for_graph_deployment_ready = AsyncMock()
    mock_api.is_deployment_ready = AsyncMock()
    return mock_api


@pytest.fixture
def mock_kube_api_class(mock_kube_api):
    mock_class = Mock()
    mock_class.return_value = mock_kube_api
    return mock_class


@pytest.fixture
def kubernetes_connector(mock_kube_api_class, monkeypatch):
    # Patch the KubernetesAPI class before instantiating the connector
    monkeypatch.setattr(
        "dynamo.planner.kubernetes_connector.KubernetesAPI", mock_kube_api_class
    )
    connector = KubernetesConnector("test-dynamo-namespace", "default")
    return connector


@pytest.mark.asyncio
async def test_add_component_increases_replicas(kubernetes_connector, mock_kube_api):
    # Arrange
    component_name = "test-component"
    mock_deployment = {
        "metadata": {"name": "test-graph"},
        "spec": {"services": {"test-component": {"replicas": 1}}},
    }
    mock_kube_api.get_graph_deployment.return_value = mock_deployment
    mock_kube_api.update_graph_replicas.return_value = None
    mock_kube_api.wait_for_graph_deployment_ready.return_value = None

    # Act
    await kubernetes_connector.add_component(component_name)

    # Assert
    mock_kube_api.get_graph_deployment.assert_called_once()
    mock_kube_api.update_graph_replicas.assert_called_once_with(
        "test-graph", component_name, 2
    )
    mock_kube_api.wait_for_graph_deployment_ready.assert_called_once_with("test-graph")


@pytest.mark.asyncio
async def test_add_component_with_no_replicas_specified(
    kubernetes_connector, mock_kube_api
):
    # Arrange
    component_name = "test-component"
    mock_deployment = {
        "metadata": {"name": "test-graph"},
        "spec": {"services": {"test-component": {}}},
    }
    mock_kube_api.get_graph_deployment.return_value = mock_deployment

    # Act
    await kubernetes_connector.add_component(component_name)

    # Assert
    mock_kube_api.update_graph_replicas.assert_called_once_with(
        "test-graph", component_name, 2
    )
    mock_kube_api.wait_for_graph_deployment_ready.assert_called_once_with("test-graph")


@pytest.mark.asyncio
async def test_add_component_deployment_not_found(kubernetes_connector, mock_kube_api):
    # Arrange
    component_name = "test-component"
    mock_kube_api.get_graph_deployment.return_value = None

    # Act & Assert
    with pytest.raises(ValueError, match="Parent DynamoGraphDeployment not found"):
        await kubernetes_connector.add_component(component_name)


@pytest.mark.asyncio
async def test_remove_component_decreases_replicas(kubernetes_connector, mock_kube_api):
    # Arrange
    component_name = "test-component"
    mock_deployment = {
        "metadata": {"name": "test-graph"},
        "spec": {"services": {"test-component": {"replicas": 2}}},
    }
    mock_kube_api.get_graph_deployment.return_value = mock_deployment

    # Act
    await kubernetes_connector.remove_component(component_name)

    # Assert
    mock_kube_api.update_graph_replicas.assert_called_once_with(
        "test-graph", component_name, 1
    )
    mock_kube_api.wait_for_graph_deployment_ready.assert_called_once_with("test-graph")


@pytest.mark.asyncio
async def test_remove_component_with_zero_replicas(kubernetes_connector, mock_kube_api):
    # Arrange
    component_name = "test-component"
    mock_deployment = {
        "metadata": {"name": "test-graph"},
        "spec": {"services": {"test-component": {"replicas": 0}}},
    }
    mock_kube_api.get_graph_deployment.return_value = mock_deployment

    # Act
    await kubernetes_connector.remove_component(component_name)

    # Assert
    mock_kube_api.update_graph_replicas.assert_not_called()
    mock_kube_api.wait_for_graph_deployment_ready.assert_not_called()


@pytest.mark.asyncio
async def test_set_component_replicas(kubernetes_connector, mock_kube_api):
    # Arrange
    target_replicas = {"component1": 3, "component2": 2}
    mock_deployment = {
        "metadata": {"name": "test-graph"},
        "spec": {
            "services": {"component1": {"replicas": 1}, "component2": {"replicas": 1}}
        },
    }
    mock_kube_api.get_graph_deployment.return_value = mock_deployment
    mock_kube_api.is_deployment_ready.return_value = True
    mock_kube_api.update_graph_replicas.return_value = None
    mock_kube_api.wait_for_graph_deployment_ready.return_value = None

    # Act
    await kubernetes_connector.set_component_replicas(target_replicas)

    # Assert
    mock_kube_api.get_graph_deployment.assert_called_once()
    mock_kube_api.is_deployment_ready.assert_called_once_with("test-graph")
    # Should be called twice, once for each component
    assert mock_kube_api.update_graph_replicas.call_count == 2
    mock_kube_api.wait_for_graph_deployment_ready.assert_called_once_with("test-graph")


@pytest.mark.asyncio
async def test_set_component_replicas_deployment_not_found(
    kubernetes_connector, mock_kube_api
):
    # Arrange
    target_replicas = {"component1": 3}
    mock_kube_api.get_graph_deployment.return_value = None

    # Act & Assert
    with pytest.raises(ValueError, match="Parent DynamoGraphDeployment not found"):
        await kubernetes_connector.set_component_replicas(target_replicas)


@pytest.mark.asyncio
async def test_set_component_replicas_empty_target_replicas(
    kubernetes_connector, mock_kube_api
):
    # Arrange
    target_replicas: dict[str, int] = {}

    # Act & Assert
    with pytest.raises(ValueError, match="target_replicas cannot be empty"):
        await kubernetes_connector.set_component_replicas(target_replicas)
