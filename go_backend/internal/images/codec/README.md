# WebP 编码资源

Status: current

`webp.wasm.gz` 原样取自 [gen2brain/webp v0.5.5](https://github.com/gen2brain/webp/tree/v0.5.5/lib)，其中的构建脚本使用 libwebp v1.4.0。对应包装源码及构建步骤见该目录的 `webp.c` 和 `Makefile`。此处只复用固定的 WASM 资源，不加载系统动态库；Go 解码仍使用既有的 `golang.org/x/image/webp`。

资源 SHA-256：`bbcf68343d79d80ba4ce7bb29215b2943711bd0787f68ff31785c6e2bb044459`。上游项目及 libwebp 的原始授权条款分别保留在 [项目许可证](LICENSE.webp) 和 [libwebp 许可证](LICENSE.libwebp)。

WASM 运行时使用 wazero v1.9.0，其原始条款保留在 [运行时许可证](LICENSE.wazero) 和 [运行时声明](NOTICE.wazero)。Docker 最终镜像将本目录的许可证及声明安装到 `/app/licenses/webp/`；单独分发二进制时也应一并提供这些文件。

[输出转换](../output_webp.go) 通过固定版本 wazero 调用其 `malloc`、`encode` ABI，保持无 CGO 构建。每次编码独立创建和关闭实例，不授予文件系统或网络能力，线性内存上限为 512 MiB；上下文取消会终止 WASM 执行。该上限只涵盖编码器线性内存，进程还持有原始解码图像及输出字节。资源不足返回图片输出错误，不发布不完整资产。质量 95、方法 4，不承诺与其他编码器的结果字节相同。
