# GPU Optimization Techniques — Survey Summary

**Source:** Hijma P., Heldens S., Sclocco A., van Werkhoven B., Bal H.E.  
"Optimization Techniques for GPU Programming."  
*ACM Computing Surveys*, Vol. 55, No. 11, Article 239, March 2023.  
DOI: [10.1145/3570638](https://doi.org/10.1145/3570638)

---

## Overview

A systematic survey of 450 articles published between 2008 and 2021, extracting and categorizing GPU software optimization techniques. The paper identifies **28 optimization techniques** organized into four themes, analyzes them against application characteristics and performance bottlenecks, and discusses the impact of architectural evolution.

---

## Themes and Techniques

### Theme 1 — Memory Access

Techniques that improve how GPU kernels use the memory hierarchy.

| # | Technique | Section | Key Idea | Representative Speedup |
|---|-----------|---------|----------|------------------------|
| 1 | **Use Dedicated Memories** | 6.1.1 | Exploit shared memory, texture cache, constant cache, and registers instead of slow global memory | 2 orders of magnitude (Tesla) |
| 2 | **Use Warp Functions** | 6.1.2 | Use warp-vote and warp-shuffle intrinsics to exchange data within a warp without shared memory | 1.2–2× (Kepler) |
| 3 | **Register Blocking** | 6.1.3 | Store repeatedly reused values in registers (temporal blocking); especially effective in 3D stencils | 2–7× (Volta) |
| 4 | **Reduce Register Usage** | 6.1.4 | Minimize register pressure to increase occupancy; avoid spilling to slow device memory | 7–9% (Kepler) |
| 5 | **Recompute** | 6.1.5 | Recompute previously computed values instead of storing them; avoids memory traffic on compute-heavy GPUs | up to 2.5× (Tesla) |
| 6 | **Coalesced Access** | 6.1.6 | Arrange thread-to-data mapping so that warp memory accesses are contiguous and aligned | 1.8–4× (Tesla) |
| 7 | **Spatial Blocking / Loop Tiling** | 6.1.7 | Partition data into tiles processed one at a time to improve cache/shared-memory locality | 3–6× (Tesla) |
| 8 | **Kernel Fusion** | 6.1.8 | Merge multiple kernels that operate on the same data into one to reduce global memory traffic and launch overhead | 10–17% (Kepler) |
| 9 | **Software Prefetching** | 6.1.9 | Issue loads early (double-buffering) to overlap memory latency with computation | 20–40% (Kepler) |
| 10 | **Compress Data** | 6.1.10 | Compress sparse indices or transfer data to reduce memory footprint and PCIe bandwidth | 1.27–5× (Pascal) |
| 11 | **Precompute** | 6.1.11 | Pre-calculate values on the CPU or in an earlier pass to avoid repeating expensive GPU computations | 49% (AMD Tahiti) |

---

### Theme 2 — Irregularity

Techniques for efficiently mapping non-uniform algorithms to the highly regular GPU architecture.

| # | Technique | Section | Key Idea | Representative Speedup |
|---|-----------|---------|----------|------------------------|
| 12 | **Loop Unrolling** | 6.2.1 | Duplicate loop body to reduce branch/address overhead, expose ILP, and enable compiler optimizations | up to 25% (Tesla) |
| 13 | **Reduce Branch Divergence** | 6.2.2 | Prevent threads in a warp from taking different paths; use predication, sorting, data padding, or algorithm redesign | 3.45× (AMD RV770) |
| 14 | **Sparse Matrix Format** | 6.2.3 | Choose or design a storage format (ELL, SELL, HYB, etc.) that maximizes coalescing and regularity for SpMV | 1.1–40× (Kepler) |
| 15 | **Kernel Fission** | 6.2.4 | Split a complex kernel into simpler ones to improve regularity, reduce register usage, or enable better auto-tuning | 70% (AMD Cypress) |
| 16 | **Reduce Redundant Work** | 6.2.5 | Avoid computing values that are not needed, especially in irregular graph or sparse-matrix workloads | 1.7–6× (Pascal) |

---

### Theme 3 — Balancing

Techniques that balance the many interrelated resource constraints of the GPU.

**3a — Instruction Stream**

| # | Technique | Section | Key Idea | Representative Speedup |
|---|-----------|---------|----------|------------------------|
| 17 | **Vectorization** | 6.3.1 | Use vector load/store types (`float4`, etc.) to widen memory transactions and reduce instruction count | 2.3–6.1× (AMD Cypress) |
| 18 | **Fast Math Functions** | 6.3.2 | Use approximate hardware intrinsics (SFUs) instead of full-precision math; trade accuracy for throughput | 3.3–4.8× (Fermi) |
| 19 | **Warp-Centric Programming** | 6.3.3 | Structure code around warps as the unit of work; reduces synchronization overhead and aids load balancing | 1.04–8× (Tesla) |

**3b — Parallelism**

| # | Technique | Section | Key Idea | Representative Speedup |
|---|-----------|---------|----------|------------------------|
| 20 | **Varying Work per Thread** | 6.3.4 | Assign more elements per thread (thread coarsening) to increase data reuse and ILP at the cost of TLP | 2–12× (AMD Tahiti) |
| 21 | **Resize Thread Blocks** | 6.3.5 | Tune thread-block dimensions to balance register usage, shared memory, and occupancy | 30× (Tesla) |
| 22 | **Auto-tuning** | 6.3.6 | Automatically search the parameter space (tile sizes, block sizes, unroll factors) to find optimal configurations | 12× (AMD Tahiti) |
| 23 | **Load Balancing** | 6.3.7 | Distribute work evenly across warps, thread blocks, and SMs; use persistent threads or work-stealing for irregular workloads | 40–60% (Turing) |

**3c — Synchronization**

| # | Technique | Section | Key Idea | Representative Speedup |
|---|-----------|---------|----------|------------------------|
| 24 | **Reduce Synchronization** | 6.3.8 | Minimize barriers by algorithmic changes, increased work per thread, or exploiting warp-level guarantees | 3–50% (Kepler) |
| 25 | **Reduce Atomics** | 6.3.9 | Avoid or aggregate atomic operations using shared memory reductions, warp shuffle, or data partitioning | 35% (Pascal) |
| 26 | **Inter-Block Synchronization** | 6.3.10 | Implement global barriers without separate kernel launches (e.g., lock-free flags, CUDA cooperative groups) to enable kernel fusion across blocks | 11–60% (Tesla) |

---

### Theme 4 — Host Interaction

Techniques that improve the interaction between the CPU host and the GPU device.

| # | Technique | Section | Key Idea | Representative Speedup |
|---|-----------|---------|----------|------------------------|
| 27 | **Host Communication** | 6.4.1 | Use pinned memory, CUDA streams, double-buffering, and pipelining to overlap data transfers with computation | 18.9% (Fermi) |
| 28 | **CPU/GPU Computation** | 6.4.2 | Divide the workload between CPU and GPU, e.g., offloading irregular tasks to the CPU | 15–20% (Fermi) |

---

## Application-Characteristic → Technique Mapping

The paper maps optimization techniques to four application properties:

### Compute-bound kernels
Reduce Redundant Work · Loop Unrolling · Varying Work per Thread · Resize Thread Blocks · Vectorization · Auto-tuning · Reduce Atomics · Fast Math Functions

### Memory-bound kernels
Use Dedicated Memories · Coalesced Access · Spatial Blocking · Register Blocking · Kernel Fusion · Software Prefetching · Use Warp Functions · Warp-Centric Programming · Reduce Synchronization · Varying Work per Thread · Resize Thread Blocks · Vectorization · Auto-tuning

### Kernels with data reuse
Use Dedicated Memories · Spatial Blocking · Register Blocking · Kernel Fusion · Inter-Block Synchronization · Use Warp Functions · Warp-Centric Programming · Varying Work per Thread · Resize Thread Blocks · Auto-tuning

### Irregular kernels
Compress Data · Reduce Branch Divergence · Sparse Matrix Formats · Kernel Fission · Reduce Redundant Work · Load Balancing

---

## Most Commonly Cited Bottlenecks

| Bottleneck | # Articles | Most Relevant Techniques |
|-----------|------------|--------------------------|
| Global memory bandwidth | 66 | Use Dedicated Memories, Coalesced Access, Kernel Fusion |
| PCIe bandwidth | 34 | Host Communication, Compress Data, Kernel Fusion/Fission |
| Global memory latency | 24 | Software Prefetching, Warp-Centric Programming |
| Uncoalesced access | 22 | Coalesced Access, Spatial Blocking, Data Layout |
| Load imbalance | 16 | Load Balancing, Reduce Branch Divergence, Warp-Centric Programming |
| Atomic contention | 16 | Reduce Atomics, Use Warp Functions, Use Dedicated Memories |
| Branch divergence | 21 | Reduce Branch Divergence, Sparse Matrix Formats, Kernel Fission |
| Utilization | 13 | Varying Work per Thread, Resize Thread Blocks, Auto-tuning |
| Register file capacity | 16 | Reduce Register Usage, Spatial/Register Blocking, Kernel Fission |

---

## Key Conclusions

1. **Techniques are highly interrelated.** Optimizing one dimension (e.g., occupancy via thread-block resizing) affects registers, shared memory, and ILP simultaneously. Reasoning in isolation is misleading.

2. **Auto-tuning is essential.** Because the optimal parameter combination depends heavily on GPU architecture and input data, automated search of the configuration space is widely needed and widely applied.

3. **Architecture matters.** Early gains from coalescing and shared memory use have diminished as L1/L2 caches improved (Fermi onward). HBM2 memory (Volta onward) reduced the need for shared memory in some workloads. Each architectural generation shifts which techniques are most impactful.

4. **No single bottleneck.** GPU utilization is governed by simultaneous constraints: TLP (occupancy), ILP, memory bandwidth, and instruction throughput. High performance requires balancing all of them—confirming the need for profiler-driven, iterative optimization.

5. **Most popular techniques** (by article mention frequency): Coalesced Access · Use Dedicated Memories · Reduce Branch Divergence · Auto-tuning.
