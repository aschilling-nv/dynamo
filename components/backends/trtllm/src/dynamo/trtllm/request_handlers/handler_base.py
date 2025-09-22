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

import copy
import logging
import os
from dataclasses import asdict, dataclass
from enum import Enum
from typing import Optional, Union

import torch
from tensorrt_llm import SamplingParams
from tensorrt_llm.llmapi import DisaggregatedParams as LlmDisaggregatedParams

from dynamo.logits_processing.examples import HelloWorldLogitsProcessor
from dynamo.nixl_connect import Connector
from dynamo.runtime.logging import configure_dynamo_logging
from dynamo.trtllm.engine import TensorRTLLMEngine
from dynamo.trtllm.logits_processing.adapter import create_trtllm_adapters
from dynamo.trtllm.multimodal_processor import MultimodalRequestProcessor
from dynamo.trtllm.publisher import Publisher
from dynamo.trtllm.utils.disagg_utils import (
    DisaggregatedParams,
    DisaggregatedParamsCodec,
)

from dynamo.llm import (
    KvPerfStats
)

configure_dynamo_logging()


class DisaggregationMode(Enum):
    AGGREGATED = "prefill_and_decode"
    PREFILL = "prefill"
    DECODE = "decode"
    ENCODE = "encode"


class DisaggregationStrategy(Enum):
    PREFILL_FIRST = "prefill_first"
    DECODE_FIRST = "decode_first"


@dataclass
class RequestHandlerConfig:
    """
    Configuration for the request handler
    """

    component: object
    engine: TensorRTLLMEngine
    default_sampling_params: SamplingParams
    publisher: Publisher
    disaggregation_mode: DisaggregationMode
    disaggregation_strategy: DisaggregationStrategy
    next_client: object
    encode_client: Optional[object] = None
    multimodal_processor: Optional[
        MultimodalRequestProcessor
    ] = None  # for multimodal support
    connector: Optional[Connector] = None


class HandlerBase:
    """
    Base class for request handlers.
    """

    def __init__(self, config: RequestHandlerConfig):
        self.engine = config.engine
        self.component = config.component
        self.default_sampling_params = config.default_sampling_params
        self.publisher = config.publisher
        self.disaggregation_mode = config.disaggregation_mode
        self.disaggregation_strategy = config.disaggregation_strategy
        self.next_client = config.next_client
        self.encode_client = config.encode_client
        self.multimodal_processor = config.multimodal_processor
        self.first_generation = True
        self.connector = config.connector

    def check_error(self, result: dict):
        """
        Check if there is an error in the result.
        """
        if self.disaggregation_mode == DisaggregationMode.PREFILL:
            return result["finish_reason"] == "error"
        else:
            return (
                result["finish_reason"] == "stop" or result["finish_reason"] == "error"
            )

    # Returns a tuple of (latency, bytes_per_second)
    def calculate_kv_perf_metrics(self, bytes, start, end):
        if bytes == 0:
            return (-1, -1)
        assert start < end, "kv cache performance start time is greater than the end time!"
        total_time = end - start
        return (total_time, bytes / total_time)
    
    # def request_perf_metrics_to_json(self, perf_metrics):
    #     timing_metrics = perf_metrics.timing_metrics
    #     kv_cache_metrics = perf_metrics.kv_cache_metrics
    #     speculative_decoding = perf_metrics.speculative_decoding
    #     metrics_json = {
    #         "first_iter": perf_metrics.first_iter,
    #         "last_iter": perf_metrics.last_iter,
    #         # exclude perf_metrics.iter since it is only meaningful when the request is not finished
    #     }
    #     metrics_json["timing_metrics"] = {
    #         "arrival_time": timing_metrics.arrival_time.total_seconds(),
    #         "first_scheduled_time": timing_metrics.first_scheduled_time.total_seconds(),
    #         "first_token_time": timing_metrics.first_token_time.total_seconds(),
    #         "last_token_time": timing_metrics.last_token_time.total_seconds(),
    #     }
    #     metrics_json["kv_cache_metrics"] = {
    #         "num_total_allocated_blocks": kv_cache_metrics.num_total_allocated_blocks,
    #         "num_new_allocated_blocks": kv_cache_metrics.num_new_allocated_blocks,
    #         "num_reused_blocks": kv_cache_metrics.num_reused_blocks,
    #         "num_missed_blocks": kv_cache_metrics.num_missed_blocks,
    #     }
    #     if timing_metrics.kv_cache_size > 0:
    #         metrics_json["timing_metrics"].update({
    #             # TODO: move to kv_cache_metrics
    #             "kv_cache_size": timing_metrics.kv_cache_size,
    #             "kv_cache_transfer_start": timing_metrics.kv_cache_transfer_start.total_seconds(),
    #             "kv_cache_transfer_start_raw": timing_metrics.kv_cache_transfer_start,
    #             "kv_cache_transfer_end": timing_metrics.kv_cache_transfer_end.total_seconds(),
    #             "kv_cache_transfer_end_raw": timing_metrics.kv_cache_transfer_end,
    #         })
    #     if speculative_decoding.total_draft_tokens > 0:
    #         metrics_json["speculative_decoding"] = {
    #             "acceptance_rate": speculative_decoding.acceptance_rate,
    #             "total_accepted_draft_tokens": speculative_decoding.total_accepted_draft_tokens,
    #             "total_draft_tokens": speculative_decoding.total_draft_tokens,
    #         }
    #     return metrics_json

    async def generate_locally(
        self, request: dict, embeddings: Optional[Union[torch.Tensor, dict]] = None
    ):
        """
        Generate responses based on the disaggregation mode in the request.

        Args:
            request: The request dictionary containing generation parameters
            embeddings: Optional tensor or dict containing embeddings for multimodal processing
        """
        logging.debug(f"Request: {request}")

        # Default to text-based input. This will be overwritten if multimodal
        # content is found and processed.
        processed_input = None

        # Check for multimodal request and process it
        if self.multimodal_processor:
            processed_input = await self.multimodal_processor.process_openai_request(
                request, embeddings
            )

        else:
            # text-only flow
            processed_input = request.get("token_ids")

        # Check if there is an error in the publisher error queue
        publishers_error = (
            self.publisher.check_error_queue() if self.publisher else None
        )
        if publishers_error:
            raise publishers_error

        # Decode the disaggregated params from the request
        disaggregated_params = None

        if self.disaggregation_mode == DisaggregationMode.PREFILL:
            request["stop_conditions"]["max_tokens"] = 1
            disaggregated_params = LlmDisaggregatedParams(request_type="context_only")

        if "disaggregated_params" in request:
            if self.disaggregation_mode == DisaggregationMode.PREFILL:
                raise ValueError("Cannot provide disaggregated_params in prefill mode")
            disaggregated_params = DisaggregatedParamsCodec.decode(
                DisaggregatedParams(**request["disaggregated_params"])
            )
            disaggregated_params.request_type = "generation_only"

        if (
            self.disaggregation_mode == DisaggregationMode.DECODE
            and disaggregated_params is None
        ):
            raise ValueError("Disaggregated params are required for decode mode")

        num_output_tokens_so_far = 0

        sampling_params = copy.deepcopy(self.default_sampling_params)

        for key, value in request["sampling_options"].items():
            if not value:
                continue
            if hasattr(sampling_params, key):
                setattr(sampling_params, key, value)

        max_tokens = request["stop_conditions"]["max_tokens"]
        if max_tokens:
            sampling_params.max_tokens = max_tokens

        ignore_eos = request["stop_conditions"].get("ignore_eos")
        if ignore_eos:
            sampling_params.ignore_eos = ignore_eos

        min_tokens = request["stop_conditions"].get("min_tokens")
        if min_tokens:
            sampling_params.min_tokens = min_tokens

        # TODO: Instead of True, we should use streaming from the request.
        # However, currently dynamo run does not send streaming in the request.
        streaming = (
            False if self.disaggregation_mode == DisaggregationMode.PREFILL else True
        )

        request_id = request.get("id") or request.get("request_id", "unknown-id")
        model_name = request.get("model", "unknown_model")

        # Optional test-only logits processing (enable with DYNAMO_ENABLE_TEST_LOGITS_PROCESSOR=1)
        if os.getenv("DYNAMO_ENABLE_TEST_LOGITS_PROCESSOR") == "1":
            processors = [HelloWorldLogitsProcessor(self.engine.llm.tokenizer)]
            adapters = create_trtllm_adapters(processors)
            sampling_params.logits_processor = adapters

        # NEW: Updated engine call to include multimodal data
        async for res in self.engine.llm.generate_async(
            inputs=processed_input,  # Use the correctly extracted inputs
            sampling_params=sampling_params,
            disaggregated_params=disaggregated_params,
            streaming=streaming,
        ):
            # TRTLLM engine needs to start generating tokens first before stats
            # can be retrieved.
            if self.first_generation and self.publisher:
                self.publisher.start()
                self.first_generation = False

            # Upon completion, send a final chunk with "stop" as the finish reason.
            # This signals to the client that the stream has ended.
            if res.finished and self.disaggregation_mode != DisaggregationMode.PREFILL:
                final_out = {}
                output = res.outputs[0]
                if output.request_perf_metrics and output.request_perf_metrics.timing_metrics:
                    timing_metrics = output.request_perf_metrics.timing_metrics
                    latency, bytes_per_second = self.calculate_kv_perf_metrics(timing_metrics.kv_cache_size,
                                                                       timing_metrics.kv_cache_transfer_start.total_seconds(),
                                                                       timing_metrics.kv_cache_transfer_end.total_seconds())
                    kv_perf_stats = KvPerfStats(
                        transfer_latency=latency,
                    )
                    self.publisher.publish_kv_perf(kv_perf_stats)
                    logging.info(f"latency={latency},bytes_per_second_for_request={bytes_per_second},trtllm_first_token_time={timing_metrics.first_token_time.total_seconds()}")
                if output.disaggregated_params:
                    final_out["ctx_request_id"] = output.disaggregated_params.ctx_request_id

                if self.multimodal_processor:
                    final_out.update(self.multimodal_processor.get_stop_response(
                        request_id, model_name
                    ))
                    yield final_out
                else:
                    item = {"finish_reason": "stop", "token_ids": []} 
                    final_out.update(item)
                    yield final_out
                break

            if not res.outputs:
                yield {"finish_reason": "error", "token_ids": []}
                break

            output = res.outputs[0]
            # The engine returns all tokens generated so far. We must calculate the new
            # tokens generated in this iteration to create the "delta".
            next_total_toks = len(output.token_ids)
            if self.multimodal_processor:
                out = self.multimodal_processor.create_response_chunk(
                    output, num_output_tokens_so_far, request_id, model_name
                )
            else:
                out = {"token_ids": output.token_ids[num_output_tokens_so_far:]}
            if output.finish_reason:
                out["finish_reason"] = output.finish_reason
            if output.stop_reason:
                out["stop_reason"] = output.stop_reason
            if self.disaggregation_mode == DisaggregationMode.PREFILL:
                # Return the disaggregated params only when operating in prefill mode.
                out["disaggregated_params"] = asdict(
                    DisaggregatedParamsCodec.encode(output.disaggregated_params)
                )
            # Yield the chunk to the client and update the token count for the next iteration.
            yield out
            num_output_tokens_so_far = next_total_toks
