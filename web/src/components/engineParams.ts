// Engine parameter catalog for the create-service form. Each supported
// serving engine (vLLM, SGLang) publishes the flags operators change most
// often; the form renders them as typed fields and passes the values to the
// server, which hands them to the controller as container arguments. A
// parameter the catalog does not list is still usable through the free-form
// "extra arguments" rows, so the catalog never limits what an engine can do.

export interface EngineParam {
  /** Flag name without the leading dashes, as the server expects it. */
  name: string;
  /** i18n key of the label. */
  labelKey: string;
  /** i18n key of the hint shown under the field. */
  hintKey: string;
  /**
   * kind picks the input: number/positive, text, a flag switch (a valueless
   * argument), or a select with the listed options.
   */
  kind: 'text' | 'number' | 'switch' | 'select';
  /** Default the form pre-fills when it is empty. */
  defaultValue?: string;
  /** Options for kind=select: the value sent to the engine. */
  options?: string[];
}

// ENGINE_PARAMS lists the common flags per engine, in the order operators
// meet them: size and placement first, then performance, then behaviour.
export const ENGINE_PARAMS: Record<string, EngineParam[]> = {
  vllm: [
    {
      name: 'max-model-len',
      labelKey: 'engine.maxModelLen',
      hintKey: 'engine.maxModelLenHint',
      kind: 'number',
    },
    {
      name: 'gpu-memory-utilization',
      labelKey: 'engine.gpuMemUtil',
      hintKey: 'engine.gpuMemUtilHint',
      kind: 'text',
      defaultValue: '0.9',
    },
    {
      name: 'tensor-parallel-size',
      labelKey: 'engine.tensorParallel',
      hintKey: 'engine.tensorParallelHint',
      kind: 'number',
      defaultValue: '1',
    },
    {
      name: 'pipeline-parallel-size',
      labelKey: 'engine.pipelineParallel',
      hintKey: 'engine.pipelineParallelHint',
      kind: 'number',
    },
    {
      name: 'dtype',
      labelKey: 'engine.dtype',
      hintKey: 'engine.dtypeHint',
      kind: 'select',
      options: ['auto', 'half', 'float16', 'bfloat16', 'float32'],
    },
    {
      name: 'quantization',
      labelKey: 'engine.quantization',
      hintKey: 'engine.quantizationHint',
      kind: 'text',
    },
    {
      name: 'max-num-seqs',
      labelKey: 'engine.maxNumSeqs',
      hintKey: 'engine.maxNumSeqsHint',
      kind: 'number',
    },
    {
      name: 'block-size',
      labelKey: 'engine.blockSize',
      hintKey: 'engine.blockSizeHint',
      kind: 'number',
    },
    {
      name: 'swap-space',
      labelKey: 'engine.swapSpace',
      hintKey: 'engine.swapSpaceHint',
      kind: 'number',
    },
    {
      name: 'enable-prefix-caching',
      labelKey: 'engine.prefixCaching',
      hintKey: 'engine.prefixCachingHint',
      kind: 'switch',
    },
    {
      name: 'trust-remote-code',
      labelKey: 'engine.trustRemoteCode',
      hintKey: 'engine.trustRemoteCodeHint',
      kind: 'switch',
    },
    {
      name: 'disable-log-requests',
      labelKey: 'engine.disableLogRequests',
      hintKey: 'engine.disableLogRequestsHint',
      kind: 'switch',
    },
  ],
  sglang: [
    {
      name: 'context-length',
      labelKey: 'engine.contextLength',
      hintKey: 'engine.contextLengthHint',
      kind: 'number',
    },
    {
      name: 'mem-fraction-static',
      labelKey: 'engine.memFractionStatic',
      hintKey: 'engine.memFractionStaticHint',
      kind: 'text',
      defaultValue: '0.9',
    },
    {
      name: 'tp-size',
      labelKey: 'engine.tensorParallel',
      hintKey: 'engine.tensorParallelHint',
      kind: 'number',
      defaultValue: '1',
    },
    {
      name: 'dp-size',
      labelKey: 'engine.dpSize',
      hintKey: 'engine.dpSizeHint',
      kind: 'number',
    },
    {
      name: 'dtype',
      labelKey: 'engine.dtype',
      hintKey: 'engine.dtypeHint',
      kind: 'select',
      options: ['auto', 'half', 'float16', 'bfloat16', 'float32'],
    },
    {
      name: 'quantization',
      labelKey: 'engine.quantization',
      hintKey: 'engine.quantizationHint',
      kind: 'text',
    },
    {
      name: 'max-running-requests',
      labelKey: 'engine.maxRunningRequests',
      hintKey: 'engine.maxRunningRequestsHint',
      kind: 'number',
    },
    {
      name: 'chunked-prefill-size',
      labelKey: 'engine.chunkedPrefill',
      hintKey: 'engine.chunkedPrefillHint',
      kind: 'number',
    },
    {
      name: 'schedule-policy',
      labelKey: 'engine.schedulePolicy',
      hintKey: 'engine.schedulePolicyHint',
      kind: 'select',
      options: ['lpm', 'random', 'fcfs', 'dfs-weight'],
    },
    {
      name: 'enable-cache-report',
      labelKey: 'engine.cacheReport',
      hintKey: 'engine.cacheReportHint',
      kind: 'switch',
    },
    {
      name: 'trust-remote-code',
      labelKey: 'engine.trustRemoteCode',
      hintKey: 'engine.trustRemoteCodeHint',
      kind: 'switch',
    },
  ],
};

// engineParams returns the catalog for an engine id ("vllm", "sglang").
// An unknown engine has no catalog: the form then offers only the
// free-form arguments.
export function engineParams(engine: string): EngineParam[] {
  return ENGINE_PARAMS[(engine || '').toLowerCase()] || [];
}
