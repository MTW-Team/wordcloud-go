# wordcloud-go

高性能纯 Go 词云生成库。输入词频和字体，生成 PNG 图片与布局信息，支持横竖混排、自定义配色、透明背景和固定随机种子。

- **纯 Go**：无需 CGO、Python 或系统字体服务。
- **紧密排版**：基于字形像素检测占用，小词可填入字形之间的空隙。
- **可复现**：相同字体、配置、词频和种子生成相同布局。
- **并发安全**：同一个 `Generator` 可供多个 goroutine 使用，生成过程支持 context 取消。

要求 Go 1.27.1 或更高版本。字体渲染使用 `golang.org/x/image`。

## 快速体验

在仓库根目录运行：

```sh
go run ./example -output wordcloud.png
```

示例内置英文词频和 Go Regular 字体，无需准备其他文件。也可以传入自己的词频和字体：

```sh
go run ./example -input words.json -font /path/to/font.ttf -output wordcloud.png -seed 42
```

`words.json` 是词语到正整数频率的映射，例如：

```json
{
  "编程": 100,
  "并发": 70,
  "性能": 50,
  "开源": 30
}
```

中文等字符需要提供包含对应字形的 TTF/OTF 字体。内置示例字体不适用于中文。

## 在项目中使用

在使用它的 Go 项目中添加依赖：

```sh
go get github.com/MTW-Team/wordcloud-go@latest
```

添加导入后运行 `go mod tidy`。以下函数使用内置字体生成英文词云，并将 PNG 写入 `io.Writer`：

```go
package example

import (
    "context"
    "io"

    "golang.org/x/image/font/gofont/goregular"
    wordcloud "github.com/MTW-Team/wordcloud-go"
)

func Render(ctx context.Context, output io.Writer) error {
    options := wordcloud.DefaultOptions()
    options.Seed = 42

    generator, err := wordcloud.New(goregular.TTF, options)
    if err != nil {
        return err
    }
    result, err := generator.Generate(ctx, map[string]int{
        "Go": 100, "concurrency": 70, "performance": 50,
        "cloud": 40, "fonts": 30, "pixels": 20,
    })
    if err != nil {
        return err
    }
    return result.WritePNG(output)
}
```

使用自定义字体时，将 `goregular.TTF` 替换为 `os.ReadFile` 读取的字体字节。`New` 复制字体和调色板数据，建议初始化一次并复用生成器，避免重复解析字体。

## 配置

从 `DefaultOptions()` 开始修改配置。部分字段的零值有特定含义，不会自动填入默认值。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `Width` / `Height` | `600` / `400` | 画布宽高，单位为像素 |
| `MaxWords` | `400` | 最多尝试排入的词数 |
| `MinFontSize` | `4` | 最小字号 |
| `MaxFontSize` | `0` | 最大字号；`0` 表示通过试排估算 |
| `FontStep` | `1` | 放不下时缩小字号的步长 |
| `Margin` | `2` | 查找空位时增加的留白 |
| `PreferHorizontal` | `0.9` | 优先横排的概率；`1` 表示只横排 |
| `RelativeScaling` | `0.5` | 词频对字号的影响，范围为 `0` 到 `1` |
| `Seed` | `0` | 随机种子 |
| `Background` | 白色 | `color.NRGBA` 背景色，支持透明度 |
| `Palette` | Viridis 风格配色 | 非空的 `[]color.NRGBA` 调色板 |

例如，生成透明背景、只横排的词云：

```go
options := wordcloud.DefaultOptions()
options.Background = color.NRGBA{}
options.PreferHorizontal = 1
```

## 输出与边界

`Generate` 返回 `Result`：

- `Image`：`*image.RGBA` 图片，可交给标准库或其他图像编码器。
- `Words`：实际排入的词及其词频、字号、字形边界、方向和颜色。
- `WritePNG(io.Writer)`：以快速无损压缩输出 PNG，并返回编码或写入错误。

画布空间不足时，实际排入词数可能少于 `MaxWords`。不同词的边界矩形可以在空白处交错，实际绘制的字形不会重叠。

空白词和非正词频会被忽略。没有有效词时返回 `ErrNoWords`；所有词都无法放入画布时返回 `ErrNoSpace`。取消 context 会中止排版；PNG 编码不接收 context。

每边尺寸最多 4096 像素，总面积不超过 1600 万像素；`MaxWords` 最大为 10000，单词最多 16384 字节。非法配置及无法解析的字体会返回错误。

调用期间不要修改输入词频。每次调用返回独立的图片和布局数据；生成器可并发共享，但调用方应根据内存和 CPU 预算限制并发任务数。

本库接受已经统计好的词频，不负责分词、停用词过滤或复杂文字 shaping，也不提供形状 mask 和 SVG 输出。

## 性能与测试

占用检测使用 64 位位图和树状索引。空位搜索先进行随机探测，再完整搜索可用位置，避免漏掉狭窄空隙。一次生成内复用字体 face，PNG 编码复用缓冲区。

实际耗时取决于字体、画布尺寸、词长、频率分布和随机种子。使用内置基准在目标机器上测量：

```sh
go test ./...
go vet ./...
go test -race ./...
go test -run '^$' -bench . -benchmem
```

竞态检测需要启用 CGO 并安装 C 编译器；库的正常构建和运行不需要 CGO。基准分别测量 50、200、400 词的生成过程，以及 PNG 编码。

## 许可证

本项目采用 [MIT License](LICENSE)，版权归 wordcloud-go 贡献者所有。

## 致谢

排版语义参考 Python WordCloud。相关上游 MIT 授权声明保留在 [WORDCLOUD-LICENSE](WORDCLOUD-LICENSE)。
