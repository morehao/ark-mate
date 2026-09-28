# buildResources（electron-builder 的 `directories.buildResources`）

这里放**打包期**才用得上的签名素材（M4-2a 起）：

- `entitlements.mac.plist`：主应用/主二进制的权限（`allow-jit` + `disable-library-validation`，
  最小集，逐条理由在文件注释里）。
- `entitlements.mac.inherit.plist`：子进程 / 嵌套对象用（codesign 的 entitlements **逐对象**生效，
  父进程的不继承）。

两个文件都是 electron-builder 在 `mac.entitlements` / `mac.entitlementsInherit` 里引用的**输入**；
判据是 `make verify/signature`（断言"plist 写了"之外，**真的签进对象里**）。

> 踩过的坑（写这份占位时的实测）：**XML 注释里不能出现连续两个连字符**——AMFI 会以
> `Failed to parse entitlements: AMFIUnserializeXML: syntax error near line N` 拒绝整次签名，
> 而 N 指的就是注释里那一行、与权限内容无关；`plutil -lint` 查不出这个错。
