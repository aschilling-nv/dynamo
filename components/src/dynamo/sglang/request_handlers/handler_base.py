# SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import asyncio
import logging
import random
import socket
from abc import ABC, abstractmethod
from contextlib import asynccontextmanager
from typing import Any, AsyncGenerator, Dict, Optional, Tuple

import sglang as sgl
from sglang.srt.utils import get_ip

from dynamo._core import Client, Component, Context
from dynamo.sglang.args import Config, DisaggregationMode
from dynamo.sglang.publisher import DynamoSglangPublisher


class BaseWorkerHandler(ABC):
    """Abstract base class for SGLang worker handlers."""

    def __init__(
        self,
        component: Component,
        engine: sgl.Engine,
        config: Config,
        publisher: Optional[DynamoSglangPublisher] = None,
        prefill_client: Optional[Client] = None,
    ) -> None:
        """Initialize base worker handler.

        Args:
            component: The Dynamo runtime component.
            engine: The SGLang engine instance.
            config: SGLang and Dynamo configuration.
            publisher: Optional metrics publisher for the worker.
            prefill_client: Optional client for prefill worker in disaggregated mode.
        """
        self.component = component
        self.engine = engine
        self.config = config
        if publisher is not None:
            self.metrics_publisher = publisher.metrics_publisher
            self.kv_publisher = publisher.kv_publisher
        else:
            self.metrics_publisher = None
            self.kv_publisher = None
        self.prefill_client = prefill_client
        self.serving_mode = config.serving_mode
        self.skip_tokenizer_init = config.server_args.skip_tokenizer_init

    @abstractmethod
    async def generate(self, request: Dict[str, Any], context: Optional[Context] = None):
        """Generate response from request.

        Args:
            request: Request dict with input and parameters.
            context: Optional context object for cancellation handling.

        Yields:
            Response data (format varies by handler implementation).
        """
        pass

    def cleanup(self) -> None:
        """Cleanup resources. Override in subclasses as needed."""
        pass

    def _get_input_param(self, request: Dict[str, Any]) -> Dict[str, Any]:
        """Get the appropriate input parameter for SGLang engine.

        Args:
            request: Request dict with token_ids or messages.

        Returns:
            Dict with either input_ids or prompt for engine.
        """
        if self.skip_tokenizer_init:
            return {"input_ids": request["token_ids"]}
        else:
            # use sglang's chat templating itself but leave tokenization to the
            # interal engine's TokenizerManager
            prompt = self.engine.tokenizer_manager.tokenizer.apply_chat_template(
                request["messages"], tokenize=False, add_generation_prompt=True
            )
            return {"prompt": prompt}

    @staticmethod
    def _generate_bootstrap_room() -> int:
        """Generate a unique bootstrap room ID for disaggregated serving.

        Returns:
            Random 63-bit integer.
        """
        return random.randint(0, 2**63 - 1)

    @staticmethod
    def _get_bootstrap_info(engine: sgl.Engine) -> Tuple[str, int]:
        """Extract bootstrap host and port from SGLang engine.

        Args:
            engine: The SGLang engine instance.

        Returns:
            Tuple of (bootstrap_host, bootstrap_port).
        """
        inner_tm = engine.tokenizer_manager
        bootstrap_port = inner_tm.server_args.disaggregation_bootstrap_port

        if inner_tm.server_args.dist_init_addr:
            bootstrap_host = socket.gethostbyname(
                inner_tm.server_args.dist_init_addr.split(":")[0]
            )
        else:
            bootstrap_host = get_ip()

        return bootstrap_host, bootstrap_port

    async def _handle_cancellation(self, sglang_request_id: str, context: Optional[Context]):
        """Background task to handle cancellation by monitoring context state."""
        if not context:
            return
        try:
            logging.debug(
                f"Cancellation monitor started for SGLang Request ID {sglang_request_id}, Context: {context.id()}"
            )
            # Wait asynchronously for cancellation signal instead of polling
            await context.async_killed_or_stopped()
            logging.info(
                f"Cancellation signal received for SGLang Request ID {sglang_request_id}, Context: {context.id()}"
            )
            # Call abort_request on the tokenizer_manager through the engine
            if (
                hasattr(self.engine, "tokenizer_manager")
                and self.engine.tokenizer_manager
            ):
                try:
                    # Use SGLang's abort_request API
                    logging.debug(
                        f"Calling SGLang abort_request for Request ID {sglang_request_id}"
                    )
                    self.engine.tokenizer_manager.abort_request(
                        rid=sglang_request_id, abort_all=False
                    )
                    is_prefill = self.serving_mode == DisaggregationMode.PREFILL
                    logging.debug(
                        f"Aborted {'Prefill ' if is_prefill else ''}Request ID: {context.id()}"
                    )
                except Exception as e:
                    logging.error(
                        f"Failed to abort SGLang request {sglang_request_id}: {e}"
                    )
            else:
                logging.error(
                    f"SGLang tokenizer_manager not found for abort request: {context.id()}"
                )
        except asyncio.CancelledError:
            # Task was cancelled, which is expected when generation completes
            logging.info(
                f"Cancellation monitor task cancelled for SGLang Request ID {sglang_request_id}, Context: {context.id()}"
            )
            pass

    @asynccontextmanager
    async def _cancellation_monitor(
        self, sglang_request_id: str, context: Optional[Context]
    ) -> AsyncGenerator[Optional[asyncio.Task], None]:
        """
        Context manager for monitoring request cancellation.
        Automatically creates a background task to monitor for cancellation and
        cleans it up when the context exits.
        Args:
            sglang_request_id: The SGLang request ID to abort if cancellation occurs
            context: Optional context object for cancellation handling
        Yields:
            asyncio.Task: The cancellation monitoring task, or None if no context
        """
        if not context:
            yield None
            return
            
        logging.info(
            f"Creating cancellation monitor task for SGLang Request ID {sglang_request_id}, Context: {context.id()}"
        )
        cancellation_task = asyncio.create_task(
            self._handle_cancellation(sglang_request_id, context)
        )

        try:
            yield cancellation_task
        finally:
            # Clean up the background cancellation task
            if not cancellation_task.done():
                logging.info(
                    f"Cancelling cancellation monitor task for SGLang Request ID {sglang_request_id}, Context: {context.id()}"
                )
                cancellation_task.cancel()
                try:
                    await cancellation_task
                except asyncio.CancelledError:
                    pass
            else:
                logging.info(
                    f"Cancellation monitor task already completed for SGLang Request ID {sglang_request_id}, Context: {context.id()}"
                )
