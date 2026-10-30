// icon-scale 把一张 PNG 按最近邻缩放出 DSM 桌面图标所需的多尺寸副本。
// DSM 的 app/config 里 icon 路径带 {0} 占位符（如 images/tad-module-{0}.png），
// 由 DSM 按需要的尺寸替换为实际文件名后缀。
//
// 用法: icon-scale -in icon_256.png -out <dir> -base tad-module
// 产出: <dir>/<base>-16.png 等
package main

import (
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
)

var sizes = []int{16, 24, 32, 48}

// nearest 缩放：图标这种小尺寸平面图形最近邻完全够用，且零依赖
func nearest(src image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	for y := 0; y < size; y++ {
		sy := b.Min.Y + y*sh/size
		for x := 0; x < size; x++ {
			sx := b.Min.X + x*sw/size
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func run() error {
	in := flag.String("in", "", "source png")
	out := flag.String("out", "", "output dir")
	base := flag.String("base", "", "output basename (files become <base>-<size>.png)")
	flag.Parse()
	if *in == "" || *out == "" || *base == "" {
		return fmt.Errorf("-in, -out and -base are required")
	}
	srcFile, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	src, err := png.Decode(srcFile)
	if err != nil {
		return fmt.Errorf("decode %s: %w", *in, err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for _, size := range sizes {
		dst := image.NewRGBA(image.Rect(0, 0, size, size))
		draw.Draw(dst, dst.Bounds(), image.Transparent, image.Point{}, draw.Src)
		draw.Draw(dst, dst.Bounds(), nearest(src, size), image.Point{}, draw.Src)
		target := filepath.Join(*out, fmt.Sprintf("%s-%d.png", *base, size))
		f, err := os.Create(target)
		if err != nil {
			return err
		}
		if err := png.Encode(f, dst); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "icon-scale:", err)
		os.Exit(1)
	}
}
