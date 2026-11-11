/**
 * Ark-Mate answerer（dsh cordis 插件）——桥接 `ask_user_question` 到 Ark-Mate 平台 elicitation。
 *
 * 背景（设计文档 2026-11-11-ask-user-answerer-bridge-design.md）：
 *   dsh 的 `ctx.userQuestions.ask()` 沿 `answerer waterfall` 找应答方。在 `mate-acp`（acp + base）
 *   形态下两条路都没人接：① agentless 需要本地 unscoped listener，② 带 agent 需要 Web answerer
 *   （只在 web-app，桌面 ACP 不渲染）。于是 `ask_user_question` 必然 `NO_PROVIDER`，前端永不弹卡。
 *
 *   本插件就是缺的那一环：注册 `user-questions/request` 监听器（unscoped + agent-scoped 各一条），
 *   把请求桥接到平台已有的 elicitation 卡片通道（POST /sessions/:id/elicitations/ask，阻塞式），
 *   用户在前端卡片作答后，平台把 accept/decline/cancel + content 返回，本插件折成 dsh 的答案形状
 *   回填 waterfall。
 *
 * 关键正确性点（对应设计 §5.2）：
 *   - **接住语义**：监听器**不调用 next()** 即否决后续链（cordis waterfall），直接 return 答案；
 *   - **agent 作用域**：unscoped（路①）与 agent-scoped（路②）都登记，选哪条由 dsh 的 ask() 决定；
 *   - **超时/取消**：尊重 request.signal，取消时抛错（对齐 dsh 的 abortedQuestion 语义）；
 *   - **如实失败**：桥接失败/端点未就绪时**抛错而非静默降级**——这是本次事故的直接教训。
 *
 * 环境（平台经 profile env 门控注入；缺任一 → 插件 disabled，dsh 自然 NO_PROVIDER，不误开）：
 *   - MATE_ANSWERER_ENDPOINT   平台 base URL（如 http://127.0.0.1:10001）
 *   - MATE_ANSWERER_TOKEN      每进程一次性令牌（平台校验；缺失则拒绝桥接）
 */

export const name = 'ark-mate-answerer';

/** 该插件只依赖 dsh 的 user-questions 服务 seam 与事件总线（都不需要额外 inject）。 */
export const inject = [];

/**
 * 不导出 `Config`：cordis 要求 `Config` 是 schemastery/standard-schema（带 `~standard.validate`），
 * 而这个插件的配置全部来自环境变量（平台注入），没有用户可调的 config 面——导出空/普通对象会让
 * cordis 的 resolveConfig 读 `Config["~standard"].validate` 时抛错（实测踩到）。所以省略。
 */

/** 只服务 unscoped（agentless）路还是也含带 agent 的路（桌面单会话两者都要）。 */
const SCOPE_AGENT = true;

/**
 * 把 dsh 的一个 userQuestions `request` 折成平台 elicitation 的 `form` 请求体。
 *
 * dsh 请求形状：{ questions: [{ id, header?, question, options?: [{label, description?}], multiSelect? }] }
 * 平台 form：{ mode:'form', message, schema: { type:'object', properties: { <qid>: {type, oneOf?, ...} } } }
 *
 * 这里做一层**忠实**转换：单选 → enum（只允许选一个标签）；多选 → array enum；无 options → string。
 * 前端卡片据此渲染；用户提交的 content 会以 `{ <qid>: <label 或 [label...]> }` 回来。
 */
function requestToElicitation(request) {
  const questions = Array.isArray(request?.questions) ? request.questions : [];
  if (questions.length === 0) {
    // dsh 不会发空问题（EMPTY_QUESTIONS 在它自己那侧拦），防御一下：也如实失败。
    throw new Error('ark-mate-answerer: 收到空 questions，无法桥接');
  }
  const properties = {};
  const required = [];
  const titles = [];
  for (const q of questions) {
    const qid = String(q.id ?? '');
    if (!qid) throw new Error('ark-mate-answerer: question 缺 id');
    const opts = Array.isArray(q.options) ? q.options : [];
    const labels = opts.map((o) => String(o.label ?? ''));
    let prop;
    if (labels.length === 0) {
      prop = { type: 'string', title: q.question ?? qid };
    } else if (q.multiSelect === true) {
      prop = { type: 'array', items: { type: 'string', enum: labels }, title: q.question ?? qid };
    } else {
      prop = { type: 'string', enum: labels, title: q.question ?? qid };
    }
    if (q.header) prop.description = String(q.header);
    // 保留每个 option 的 description，供卡片显示 tradeoff（放在 x-options 扩展里，前端可选读）。
    if (opts.some((o) => o.description)) prop['x-options'] = opts.map((o) => ({ label: String(o.label ?? ''), description: o.description ? String(o.description) : undefined }));
    properties[qid] = prop;
    required.push(qid); // 每个问题默认必答（跳过由前端以空 selected 表达，见 dsh 文档）
    titles.push(q.header ? `${q.header}` : (q.question ?? qid));
  }
  const message = titles.join(' / ') || '请回答以下问题';
  return { mode: 'form', message, schema: { type: 'object', properties, required } };
}

/**
 * 把平台返回的 outcome 折成 dsh 期望的「答案」形状。
 *
 * dsh 期望每个问题一条 `{ id, selected: string[], custom?: string }`（见 dsh-tool-ask-user README）。
 * 平台回来的是 `accept + content: { <qid>: <label 或 [label...]> }`。
 *   - accept：逐问题映射成 selected（多选数组→selected；单选定值→selected:[值]）；
 *   - decline/cancel：抛错（dsh 侧会当作未回答；模型由此得到 `ask_user_question ...` 失败并如实处理）。
 */
function outcomeToAnswer(request, outcome) {
  const action = String(outcome?.action ?? '');
  if (action !== 'accept') {
    throw new Error(`ark-mate-answerer: 用户未作答（${action || 'unknown'}）`);
  }
  const content = outcome?.content ?? {};
  const questions = Array.isArray(request?.questions) ? request.questions : [];
  // **契约关键**：dsh 的 answerer 返回值必须是 `{ answers: [...] }`（dsh-tool-ask-user 的
  // answerResult 会做 `result.answers.map(...)`）。返回裸数组会触发 `undefined.map` 报错（实测踩到）。
  const answers = questions.map((q) => {
    const qid = String(q.id ?? '');
    const v = content[qid];
    if (Array.isArray(v)) {
      return { id: qid, selected: v.map((x) => String(x)) };
    }
    if (v === undefined || v === null || v === '') {
      return { id: qid, selected: [] }; // 用户跳过该题
    }
    return { id: qid, selected: [String(v)] };
  });
  return { answers };
}

/** 从 dsh agent 对象或请求里取原生会话 id（带 rt_ 壳）；取不到返回空串（平台侧再兜底）。 */
/**
 * 从 dsh 的 agent/request 里取**原生会话 id**（平台会补 `rt_` 壳反查成平台会话）。
 *
 * 实测（真机联调）：`request.agent.session` 是**对象**（会话投影），其 `.id` 才是原生会话 id，
 * 且与 `agent.id` 相同；平台把它加 `rt_` 壳存成 runtime_session_id。所以���级取：
 * `agent.session.id` → `agent.id` → 兼容若干字符串字段。取不到返回空串（平台侧如实 40401）。
 */
function nativeSessionId(request) {
  const agent = request?.agent;
  if (agent) {
    if (typeof agent.session === 'string') return agent.session;
    if (agent.session && typeof agent.session.id === 'string') return agent.session.id;
    if (typeof agent.id === 'string') return agent.id;
  }
  if (typeof request?.session === 'string') return request.session;
  if (request?.session && typeof request.session.id === 'string') return request.session.id;
  if (typeof request?.sessionId === 'string') return request.sessionId;
  return '';
}

/**
 * 桥接一次请求到平台：POST /sessions/-/elicitations/ask（阻塞等用户决策）。
 * 路径会话用 `-` 占位，真实归属由 body 的 runtime_session_id 反查（映射留在平台侧）。
 */
async function bridge(cfg, request, signal) {
  if (!cfg.endpoint || !cfg.token) {
    throw new Error('ark-mate-answerer: 平台端点/令牌未注入（MATE_ANSWERER_ENDPOINT/TOKEN），如实失败');
  }
  const body = {
    ...requestToElicitation(request),
    runtime_session_id: nativeSessionId(request),
    // **限时问答标记**（dsh `askTimed` 透传的 `request.wait.timed`）：为真时告诉平台「这次问的是
    // 限时问题」——平台据此把「这一路桥接断开（底座计时器到期主动中止）」当作「超时转 pending、卡片
    // 保留可回答」而不是取消。用户随后仍可作答，回答作为**新一轮**用户消息进入（ADR-11）。
    // 缺省 false = 阻塞式（平台断开即取消）。
    timed: request?.wait?.timed === true,
  };
  const url = `${cfg.endpoint.replace(/\/+$/, '')}/v1/mate/sessions/-/elicitations/ask`;
  let resp;
  try {
    resp = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Mate-Answerer-Token': cfg.token },
      body: JSON.stringify(body),
      signal,
    });
  } catch (err) {
    // 网络/abort：如实失败（abort 时上层会按 signal.reason 处理）。
    throw err;
  }
  if (!resp.ok) {
    let detail = '';
    try { detail = await resp.text(); } catch { /* ignore */ }
    throw new Error(`ark-mate-answerer: 平台应答 ${resp.status} ${detail}`.trim());
  }
  const env = await resp.json().catch(() => null);
  // 平台统一信封 { code, data }；code!=0 视为失败。
  const data = env && typeof env === 'object' && 'data' in env ? env.data : env;
  if (env && typeof env.code === 'number' && env.code !== 0) {
    throw new Error(`ark-mate-answerer: 平台返回错误 code=${env.code} ${env.message ?? ''}`.trim());
  }
  return data;
}

/** cordis 插件体：注册 unscoped + agent-scoped 两条 user-questions/request 监听。 */
export function apply(ctx, config) {
  // 配置全部来自环境变量（平台经 profile env 注入）；`config` 参数保留但通常为空。
  const cfg = {
    endpoint: (config && config.endpoint) || process.env.MATE_ANSWERER_ENDPOINT || '',
    token: (config && config.token) || process.env.MATE_ANSWERER_TOKEN || '',
  };

  const handler = async (request, _next) => {
    // **不调用 next()**：接住并终结 waterfall 链（cordis：不调 next = veto 掉后续 base 行为）。
    const signal = request?.signal;
    if (signal?.aborted) {
      // 已取消：抛错（对齐 dsh abortedQuestion）。
      throw new Error('ark-mate-answerer: 请求在桥接前已被取消');
    }
    const outcome = await bridge(cfg, request, signal);
    return outcomeToAnswer(request, outcome);
  };

  // 路①：agentless 的 unscoped waterfall（dsh ask() 在无 agent 时走这条）。
  ctx.on('user-questions/request', handler);

  // 路②：agent-scoped waterfall（dsh ask() 带 agent 时走 scopeTarget(agent, agent)）。
  // 桌面单会话即可覆盖；多条监听不冲突——同一请求只会命中其中一条 waterfall。
  if (SCOPE_AGENT) {
    ctx.on('user-questions/request', handler, { global: true });
  }
}

// 纯逻辑导出（仅供单测/内省；生产由 cordis loader 用 name/inject/apply）。
export const __test__ = { requestToElicitation, outcomeToAnswer, nativeSessionId };
