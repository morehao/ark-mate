package runtime

// ——— 模型值投影端口（注册表的第三张可执行端口表） ———
//
// 背景：平台清单里的一条是 `(provider_id, model_id)` 这一对**平台词**，而底座配置文件里那一格
// 要的是**底座自己认得的不透明值**：dsh 是 `["<provider>","<model>"]` 这个 JSON 数组，另一家
// 可能就是 `<provider>/<model>` 这一串。平台既不解析它、也不反推它——它只存**生成 value 的输入**
// （provider_id + model_id），生成规则由该底座声明，于是"下发"（T2）与"物化"（T5）不可能分叉。
//
// # 为什么必须是一个端口而不是内核里的 switch
//
// 这条转换此前写在内核里，是一段 `switch runtimeID`：
//
//	case "dsh":      json.Marshal([]string{providerID, modelID})
//	case "opencode": providerID + "/" + modelID
//	default:         model_value_unsupported
//
// 那个 `default` 分支是"内核认识具体底座"的教科书形态：接第三个底座的人跑到这里，唯一自然的
// 动作是**回内核加一个 case**——而那正是"没有 adapter 层"的定义。它的失效还特别安静：所有既有
// 守卫都是绿的（这不是配置格式字面量，静态扫描抓的是另一类东西），只有下一个人加底座时才撞上。
//
// 收口后由 archtest 的 `TestInternalHasNoRuntimeIDSpecialCase` 钉住：`internal/**` 里不得再出现
// 任何**内置底座 id** 的 `case` / 比较特判。于是"接一个新底座 = 新建一个目录 + 一个 `xxxSpec()`
// + 挂端口实现"这句话对**这一条链路**也成立，而不只是对隔离表与原生权限成立。
//
// 形状沿用 ADR-2（`Spec` 只装纯数据、可执行对象挂注册表），签名只用**内建类型**：接口定义在父包，
// 而 adapter 子包不能 import 父包（约束⑦ 的父子 import 环），所以 adapter 只要写出同签名的方法
// 就结构化满足它，不必 import 任何父包类型。
type ModelValueAdapter interface {
	// Value 把平台清单里的一条编成**该底座认得的不透明值**。
	//
	// 返回错误 = 该形状无法表示这一条（例如底座要求的分隔符恰好出现在 id 里）。内核据此把这条
	// 声明当作"不可交付"如实上报，而不是塞一个它自己都不认识的值下去。
	Value(providerID, model string) (string, error)
}

// RegisterModelValue 登记某 runtime 的模型值生成实现（同 ID 后注册覆盖）。
func (r *Registry) RegisterModelValue(id string, a ModelValueAdapter) {
	if r == nil {
		return
	}
	if r.modelValues == nil {
		r.modelValues = map[string]ModelValueAdapter{}
	}
	r.modelValues[id] = a
}

// ModelValue 取某 runtime 的模型值生成实现。
//
// `ok=false` 的语义是**该底座不消费平台的模型清单**（不是"登记漏了"）：调用方据此回空串
// （"不下发默认模型"）或跳过该条（"不猜它认什么形状"），而不是报一个假失败。
func (r *Registry) ModelValue(id string) (ModelValueAdapter, bool) {
	if r == nil || r.modelValues == nil {
		return nil, false
	}
	a, ok := r.modelValues[id]
	return a, ok && a != nil
}
