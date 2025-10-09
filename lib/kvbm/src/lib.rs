// SPDX-FileCopyrightText: Copyright (c) 2024-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
use pyo3::exceptions::{PyRuntimeError, PyTypeError};
use pyo3::types::{PyCapsule, PyCapsuleMethods};
use pyo3::{exceptions::PyException, prelude::*};
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
    #[cfg(feature = "block-manager")]
    block_manager::add_to_module(m)?;

    Ok(())
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
