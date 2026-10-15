// Package version 提供产品的单一版本源。
//
// 客户端(internal/update.CurrentVersion)与云服务(cloud/main.go serverVersion)
// 都引用此处的 Version, 避免版本号在多处硬编码分叉。本包刻意保持零依赖、
// 跨平台(不含任何 Windows 专有代码), 以便 Linux 云服务二进制也能安全导入。
//
// 发布纪律: 发新版时只需改这一处 Version(连同 README 与官网/Gitee Release),
// 客户端与云端报告的产品版本即自动保持一致。
package version

// Version 产品发布版本(语义化, 与 Release tag 对齐)。
const Version = "1.1.4"
