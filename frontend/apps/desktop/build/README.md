# buildResources（electron-builder 的 `directories.buildResources`）。
#
# 这里放的是**打包期**才用得上的签名素材：M4-2a 会加 `entitlements.mac.plist`
# （JIT 相关最小集 + hardened runtime 下的 library validation 处置）。
# 目录必须存在（否则 electron-builder 会警告找不到 buildResources），故先落这个占位。
