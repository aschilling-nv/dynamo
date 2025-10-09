// SPDX-FileCopyrightText: Copyright (c) 2024-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
use pyo3::exceptions::{PyRuntimeError, PyTypeError};
use pyo3::types::{PyCapsule, PyCapsuleMethods};
use pyo3::{exceptions::PyException, prelude::*};
use std::sync::OnceLock;
use std::sync::Weak;
use std::{fmt::Display, sync::Arc};
use tokio::sync::Mutex;

use dynamo_runtime::{self as rs, logging, traits::DistributedRuntimeProvider};

use dynamo_llm::{self as llm_rs};

mod block_manager;

/// A Python module implemented in Rust. The name of this function must match
/// the `lib.name` setting in the `Cargo.toml`, else Python will not be able to
/// import the module.
#[pymodule]
fn _core(m: &Bound<'_, PyModule>) -> PyResult<()> {
    logging::init();

    init_pyo3_tokio_rt();
    #[cfg(feature = "block-manager")]
    block_manager::add_to_module(m)?;

    Ok(())
}

static PYO3_TOKIO_INIT: OnceLock<()> = OnceLock::new();
static PYO3_TOKIO_RT: OnceLock<tokio::runtime::Runtime> = OnceLock::new();

fn init_pyo3_tokio_rt() {
    PYO3_TOKIO_INIT.get_or_init(|| {
        // Build a minimal multi-thread runtime (1 worker, 1 blocking)
        let workers = std::thread::available_parallelism()
            .map(|n| n.get())
            .unwrap_or(1);

        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(workers)
            .max_blocking_threads(512)
            .enable_all()
            .build()
            .expect("failed to build fallback tokio runtime for pyo3_async_runtimes");

        // Store it so it lives for the entire process
        let _ = PYO3_TOKIO_RT.set(rt);
        let rt_ref = PYO3_TOKIO_RT.get().expect("runtime missing after set");

        // Initialize pyo3-async runtimes with this runtime
        let _ = pyo3_async_runtimes::tokio::init_with_runtime(rt_ref);
        // (Re-inits are ignored thanks to OnceLock guarding this block)
    });
}

pub fn to_pyerr<E>(err: E) -> PyErr
where
    E: Display,
{
    PyException::new_err(format!("{}", err))
}

#[pyclass]
#[derive(Clone)]
struct Component {
    inner: rs::component::Component,
}

pub fn extract_distributed_runtime_from_obj(
    py: Python<'_>,
    drt_obj: PyObject,
) -> PyResult<Arc<rs::DistributedRuntime>> {
    let obj = drt_obj.bind(py);

    let cls = py.import("dynamo._core")?.getattr("DistributedRuntime")?;
    if !obj.is_instance(&cls)? {
        return Err(PyTypeError::new_err(
            "expected dynamo._core.DistributedRuntime",
        ));
    }

    let cap_any = obj.call_method0("to_capsule")?;
    let cap: &Bound<'_, PyCapsule> = cap_any.downcast()?; // borrow
    let weak: &Weak<rs::DistributedRuntime> = unsafe { cap.reference::<Weak<_>>() };

    weak.upgrade()
        .ok_or_else(|| PyRuntimeError::new_err("runtime is no longer alive"))
}
