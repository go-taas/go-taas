# 推理引擎参数模板 —— 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 功能点 | 运维人员可以把一组推理引擎参数连同其适用的部署上下文保存为具名模板，部署表单直接载入最匹配的模板，无需重复输入参数；模板支持增删改查以及批量导入导出 |
| 文档范围 | 模板模型与上下文语义、七个 RPC、匹配与排序、校验规则、导出文档与导入策略、控制台交互、错误码与验收标准 |
| 所属模块 | `services/infer`（`template_model.go`、`template_repository.go`、`template_service.go`） · `proto/taas/infer/v1` · `web`（`EngineTemplatesPage`、`DeployForm`、`shells/nav`） |
| 关联文档 | [StormService 服务拓扑](./stormservice-serving-topology.zh-cn.md)（模板保存的拓扑与按角色参数） · [加速卡清单](./accelerator-inventory.zh-cn.md)（上下文取值的来源） |
| 状态 | 已实现 |

---

## 1. 为什么需要模板

部署一个模型意味着一堆引擎参数：`max-model-len`、`gpu-memory-utilization`、`enable-chunked-prefill`、`tensor-parallel-size`，在多机或 PD 分离部署下还要再来两套。同一模型在同一卡型上想要的值永远一样，而这恰恰是最容易写错的部分。模板把这些值记录一次，并标注它们在什么上下文中才是正确的。

## 2. 模板模型

`engine_param_templates`

| 列 | 类型 | 含义 |
| --- | --- | --- |
| `template_id` | uuid | 主键 |
| `name` | varchar(128)，唯一 | 运维人员引用模板的名字 |
| `description` | varchar(512) | 自由文本 |
| `engine` | varchar | 参数所属推理引擎（`vllm`、`sglang`） |
| `model_id` | varchar(64) | 模板调优针对的模型；空 = 任意 |
| `topology` | varchar | `single` / `multinode` / `pd`；空 = 任意 |
| `accelerator` | varchar | 加速卡厂商；空 = 任意 |
| `accelerator_type` | varchar | 卡型；空 = 任意 |
| `engine_args` | json | 公共参数，键为**不带**前缀横线的参数名 |
| `extra_args` | json | 自由参数列表，追加在公共参数之后 |
| `role_engine_args` | json | 按角色覆盖（`worker` / `prefill` / `decode`） |
| `created_at` / `updated_at` / `updated_by` | | 审计列 |

`model_id` 刻意使用文本而非 `uuid`：空字符串表示「任意模型」，而 Postgres 不接受 `uuid` 列写入 `''`（见 AD1）。

**AD1 —— 上下文用「空值」而不是 NULL 表示不限定。** 每个上下文字段都是非空字符串，空值表示「匹配一切」。同一个哨兵既承担「任意」语义，也承担查询语义，于是过滤条件永远是 `value = ? OR value = ''`，任何地方（包括导出/导入往返）都不需要 `IS NULL` 分支。代价是形似外键的列必须用 `varchar`：早期版本把 `model_id` 建成 `uuid`，服务在 AutoMigrate 阶段因 Postgres 拒绝 `''` 而崩溃重启。

**AD2 —— 引擎严格匹配，其余宽松匹配。** vLLM 的参数对 SGLang 毫无意义，因此引擎必须精确相等：引擎不同的模板永远不会被返回。其余上下文字段是偏好而非要求，所以用 `= ? OR = ''`。

## 3. RPC

| RPC | HTTP | 用途 |
| --- | --- | --- |
| `CreateEngineParamTemplate` | `POST /api/v1/admin/engine-param-templates` | 创建 |
| `ListEngineParamTemplates` | `GET /api/v1/admin/engine-param-templates` | 列表，可按匹配上下文收窄，最佳匹配在前 |
| `GetEngineParamTemplate` | `GET /api/v1/admin/engine-param-templates/{template_id}` | 读取单个 |
| `UpdateEngineParamTemplate` | `PATCH /api/v1/admin/engine-param-templates/{template_id}` | 覆盖内容 |
| `DeleteEngineParamTemplate` | `DELETE /api/v1/admin/engine-param-templates/{template_id}` | 删除 |
| `ExportEngineParamTemplates` | `GET /api/v1/admin/engine-param-templates:export` | 全部模板导出一个 JSON 文档 |
| `ImportEngineParamTemplates` | `POST /api/v1/admin/engine-param-templates:import` | 导入该文档 |

七个接口全部属于**管理面**（`/api/v1/admin/...`）：模板描述的是平台硬件与引擎行为，而不是某个租户的工作，且消费它的部署表单位于平台控制台。因此控制台的 `pathBelongsToRealm` 守卫会拒绝以 `/api/v1/...` 发起的上述调用。

`ListEngineParamTemplates` 以查询字段携带匹配上下文（`engine`、`model_id`、`topology`、`accelerator`、`accelerator_type`），并为每个模板返回只读的 `match_score`；不带上下文时返回全部且 `match_score = 0`。

## 4. 匹配

仓储层构造如下谓词：

```sql
engine = :engine
AND (model_id = :model_id OR model_id = '')
AND (topology = :topology OR topology = '')
AND (accelerator = :accelerator OR accelerator = '')
AND (accelerator_type = :accelerator_type OR accelerator_type = '')
```

服务层再对结果打分，越具体越靠前：

| 上下文字段 | 分值 |
| --- | --- |
| `model_id` | 8 |
| `topology` | 4 |
| `accelerator` / `accelerator_type` | 各 2 |

「模型 + 拓扑 + 卡型」的模板得 16 分；一个只有「vLLM 默认值」的模板得 0 分，但仍然出现在最后。模型权重最高，因为针对某模型调出来的值几乎不可迁移，而拓扑或卡型的提示通常可以。

排序为 `match_score DESC, name ASC`，因此同分时稳定，列表不会在请求之间跳序。

## 5. 校验

`validateTemplate` 会以 10322 并指明字段的方式拒绝：

- 为空或仅含空白的 `name`；
- 为空的 `engine`；
- 不在 `single` / `multinode` / `pd` 内的 `topology`（历史值 `distributed` 会像其他地方一样归一化为 `multinode`）；
- 已带前缀横线或含 `=` 的 `engine_args` 键 —— 键是参数名本身，值是参数值；
- 不在 `worker` / `prefill` / `decode` 内的 `role_engine_args` 角色。

重名返回 10321；get/update/delete 未命中返回 10320。

| 错误码 | 含义 |
| --- | --- |
| 10320 | 模板不存在 |
| 10321 | 模板名称已存在 |
| 10322 | 模板内容非法 |

## 6. 导出与导入

`ExportEngineParamTemplates` 生成一个带版本号的信封，使一套调优可以提交到 git、评审、并在不同部署之间迁移：

```json
{
  "version": 1,
  "exportedAt": "2025-02-11T09:12:44Z",
  "templates": [
    {
      "name": "Qwen 单机 4090",
      "description": "…",
      "engine": "vllm",
      "modelId": "…",
      "topology": "single",
      "accelerator": "NVIDIA",
      "acceleratorType": "NVIDIA-GeForce-RTX-4090",
      "engineArgs": { "max-model-len": "8192", "gpu-memory-utilization": "0.9" },
      "extraArgs": ["--enable-chunked-prefill"],
      "roleEngineArgs": []
    }
  ]
}
```

导入接收该负载与 `strategy`：

| 策略 | 名称已存在时 |
| --- | --- |
| `skip`（默认） | 保持原样，计入 `skipped` |
| `overwrite` | 原地替换，计入 `overwritten` |

新名称在两种策略下都会被创建。响应报告 `created`、`overwritten`、`skipped`，运维人员因此能区分「空导入」与真正生效的导入。写入前会先校验 `version`：未知版本会让整个导入失败，而不是导入半个文档。

## 7. 控制台

`/admin/engine-templates`（`EngineTemplatesPage`）以一行可读文本展示模板上下文 —— 引擎、模型显示名或 `任意模型`、拓扑、卡型 —— 并给出一句提示：公共参数个数，以及是否存在按角色覆盖。创建/编辑在弹窗中完成，复用部署表单的参数编辑器；页面同时提供导出（下载 JSON）与导入（上传并选择策略）。

部署表单（`DeployForm`）在引擎参数上方有**参数模板**下拉框。它以当前所选引擎/模型/拓扑/卡型为上下文调用 `ListEngineParamTemplates`，因此下拉里只会出现能适用的模板，最佳匹配在前。选中后填入公共参数、自由参数以及当前可见角色的参数，并提示 `已载入模板。下方仍可继续调整，服务会保存自己的副本。` —— 载入模板是复制而非引用：之后编辑模板绝不会改变已经部署的服务，服务自身像手工填写一样保存自己的参数。

## 8. 验收标准

1. 用重名创建模板返回 10321；`engine` 为空或拓扑未知返回 10322。
2. 引擎与查询不一致的模板永不返回；模型/拓扑/卡型上下文为空的模板总会返回。
3. `match_score` 能把「全上下文」模板排在「仅拓扑」之上，再之上于「无上下文」。
4. 导出得到的文档导入到空环境后，`created` 等于导出条数。
5. 用 `skip` 重复导入同一文档不创建任何模板；用 `overwrite` 则全部替换且 `skipped` 为 0。
6. 在部署表单中选择模板后，引擎参数输入框与自由参数行被填满。
7. 源模板被修改或删除后，已部署服务的参数保持不变。

已在 https://taas.sudoinfotech.com 实际验证：通过 API 完成模板的列表/创建/匹配/导出，模板页面正常渲染，部署表单载入模板后 `max-model-len` 填入 8192、`gpu-memory-utilization` 填入 0.9，并出现 `--enable-chunked-prefill` 一行。
