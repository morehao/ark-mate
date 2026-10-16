package manager

// ——— 技能定义写面（已清空：实现全在 `domain/skill`）———
//
// 写面主体（顺序与失败处理、校验闸门、载体 CAS、内存入表、物化重校）在 `domain/skill.Plane` 的
// `WriteSkill` / `DeleteSkill` 里——域拥有"技能定义怎么生效"这条规则。
//
// 本文件在 P4a 正文第三十七刀之前只剩三条一行转发（`CreateSkill` / `UpdateSkill` / `DeleteSkill`），
// 那一刀把接口声明与转发一起删掉，控制器直接 `h.skills.WriteSkill(...)` /
// `h.skills.DeleteSkill(...)`。留这条说明是因为**函数删了、文件还在**：下一个人看到空文件才知道
// 这里的实现去哪了，而不是以为漏搬了。
