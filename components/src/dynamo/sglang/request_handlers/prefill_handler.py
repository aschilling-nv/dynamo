# SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import asyncio
import logging
from typing import Any, AsyncGenerator, Dict, Optional

import sglang as sgl

from dynamo._core import Component, Context
from dynamo.sglang.args import Config
from dynamo.sglang.request_handlers.handler_base import BaseWorkerHandler


class PrefillWorkerHandler(BaseWorkerHandler):
    """Handler for prefill workers in disaggregated serving mode."""

    def __init__(
        self, component: Component, engine: sgl.Engine, config: Config
    ) -> None:
        """Initialize prefill worker handler.

        Args:
            component: The Dynamo runtime component.
            engine: The SGLang engine instance.
            config: SGLang and Dynamo configuration.
        """
        self.engine = engine
        self.bootstrap_host, self.bootstrap_port = self._get_bootstrap_info(self.engine)
        super().__init__(component, engine, config)
        logging.info(
            f"Prefill worker handler initialized - bootstrap host: {self.bootstrap_host}, bootstrap port: {self.bootstrap_port}"
        )

    def cleanup(self) -> None:
        """Shutdown the prefill engine and cleanup resources."""
        self.engine.shutdown()
        logging.info("Prefill engine shutdown")
        super().cleanup()

    async def generate(
        self, request: Dict[str, Any], context: Optional[Context] = None
    ) -> AsyncGenerator[Dict[str, Any], None]:
        """Generate prefill output and provide bootstrap info for decode worker.

        Args:
            request: Request dict with 'request' and 'sampling_params' keys.
            context: Optional context object for cancellation handling.

        Yields:
            Bootstrap info dict with host, port, and room for decode worker connection.
        """
        if context:
            logging.debug(f"New Request ID: {context.id()}")
        bootstrap_room = self._generate_bootstrap_room()

        bootstrap_info = {
            "bootstrap_host": self.bootstrap_host,
            "bootstrap_port": self.bootstrap_port,
            "bootstrap_room": bootstrap_room,
        }

        yield bootstrap_info

        input_param = self._get_input_param(request["request"])

        results = await self.engine.async_generate(
            **input_param,
            sampling_params=request["sampling_params"],
            stream=True,
            bootstrap_host=self.bootstrap_host,
            bootstrap_port=self.bootstrap_port,
            bootstrap_room=bootstrap_room,
        )

        asyncio.create_task(self._consume_results(results, context))

    async def _consume_results(
        self, results: AsyncGenerator[Any, None], context: Optional[Context] = None
    ) -> None:
        """Consume async generator results without processing.

        Args:
            results: Async generator from engine.async_generate.
            context: Optional context object for cancellation handling.
        """
        cancellation_task = None

        async for res in results:
            # Extract SGLang request ID from the first response and start cancellation monitoring
            if context and cancellation_task is None:
                meta_info = res.get("meta_info", {})
                sglang_request_id = meta_info.get("id")
                if sglang_request_id:
                    # Now we have the request ID, start the cancellation monitor
                    cancellation_context = self._cancellation_monitor(
                        sglang_request_id, context
                    )
                    cancellation_task = await cancellation_context.__aenter__()

            # Check for cancellation on each iteration
            if context and (context.is_stopped() or context.is_killed()):
                logging.info(f"Aborted Prefill Request ID: {context.id()}")
                break

        # Clean up cancellation monitor if it was created
        if cancellation_task is not None:
            try:
                await cancellation_context.__aexit__(None, None, None)
            except Exception as e:
                logging.error(
                    f"Error cleaning up cancellation monitor for prefill stream: {e}"
                )
