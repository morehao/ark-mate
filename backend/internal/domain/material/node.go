// Package material 是「资料库」领域的**纯值对象与判决**（leaf 包）：节点种类枚举、树不变量
// （防环）、节点种类与载体的匹配校验。它不 import 应用内核的任何包（不 import `internal/runtime`、
// 不 import `persistence/model`、不 import 任何 `svc*`）——这一层只表达「一棵资料库目录树长什么样、
// 什么写法不合法」，落库/编排/HTTP 各自在别的层。
//
// 本期范围（2026-11-03 拍板）：**只做资料库、不做知识库**。因此本包**没有**任何语义检索 /
// 向量 / 问答相关概念——「找」只到「关键词子串匹配」这一层（由用例层对 title/body 做 LIKE）。
package material

// NodeKind 节点种类（封闭集合，决定载体列与读取入口）。与 `persistence/model` 的同名常量为
// **同一枚举的两处登记**（model 侧为落库字面量）；两处必须一致，由 archtest 的载体一致性守卫
// （M-4）与列类型快照（G1）共同钉住。
type NodeKind string

const (
	// KindFolder 纯容器目录，无正文。
	KindFolder NodeKind = "folder"
	// KindDoc 文档（Body 承载正文）。
	KindDoc NodeKind = "doc"
	// KindTable 结构化表格（TableID 指向 library_tables）。
	KindTable NodeKind = "table"
	// KindLink 网页剪藏（Body + SourceURL）。
	KindLink NodeKind = "link"
	// KindDrive 网盘文件（DriveUploadID 指向附件链路的 core_file_upload.id）。
	KindDrive NodeKind = "drive"
	// KindPage HTML 页面（Body 承载 HTML）。
	KindPage NodeKind = "page"
)

// AllNodeKinds 合法种类集合（写面校验用：非法取值报 `material_upload_kind_unsupported`）。
var AllNodeKinds = []NodeKind{KindFolder, KindDoc, KindTable, KindLink, KindDrive, KindPage}

// IsValidNodeKind 判断一个种类是否在封闭集合内。
func IsValidNodeKind(k NodeKind) bool {
	for _, v := range AllNodeKinds {
		if v == k {
			return true
		}
	}
	return false
}

// UploadKinds 可通过 `library_upload`（按扩展名）建出的种类——folder 是纯容器、不走上传；
// 具体扩展名映射在用例层（md→doc、csv→table、html→page、其余→drive）。
var UploadKinds = []NodeKind{KindDoc, KindTable, KindPage, KindDrive}

// IsUploadKind 判断种类是否可由上传路径产生。
func IsUploadKind(k NodeKind) bool {
	for _, v := range UploadKinds {
		if v == k {
			return true
		}
	}
	return false
}
