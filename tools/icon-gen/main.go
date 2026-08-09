package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", ".", "output directory")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		panic(err)
	}
	for _, size := range []int{64, 256} {
		img := drawIcon(size)
		path := filepath.Join(*out, "icon_"+itoa(size)+".png")
		file, err := os.Create(path)
		if err != nil {
			panic(err)
		}
		if err := png.Encode(file, img); err != nil {
			file.Close()
			panic(err)
		}
		if err := file.Close(); err != nil {
			panic(err)
		}
	}
}

func drawIcon(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	transparent := color.RGBA{0, 0, 0, 0}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, transparent)
		}
	}
	s := float64(size)
	center := func(x, y float64) (float64, float64) { return x * s, y * s }
	cx, cy := center(.5, .5)
	outer := color.RGBA{23, 202, 142, 255}
	inner := color.RGBA{7, 26, 21, 255}
	bolt := color.RGBA{210, 255, 235, 255}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+.5-cx, float64(y)+.5-cy
			r := s * .44
			if dx*dx+dy*dy <= r*r {
				img.Set(x, y, outer)
			}
			r2 := s * .35
			if dx*dx+dy*dy <= r2*r2 {
				img.Set(x, y, inner)
			}
		}
	}
	shield := [][2]float64{{.50, .20}, {.73, .29}, {.70, .58}, {.50, .79}, {.30, .58}, {.27, .29}}
	fillPolygon(img, shield, outer)
	lightning := [][2]float64{{.53, .29}, {.39, .52}, {.49, .52}, {.44, .70}, {.63, .44}, {.52, .44}}
	fillPolygon(img, lightning, bolt)
	return img
}

func fillPolygon(img *image.RGBA, points [][2]float64, c color.Color) {
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			px, py := (float64(x)+.5)/w, (float64(y)+.5)/h
			inside := false
			j := len(points) - 1
			for i := 0; i < len(points); i++ {
				xi, yi := points[i][0], points[i][1]
				xj, yj := points[j][0], points[j][1]
				if (yi > py) != (yj > py) && px < (xj-xi)*(py-yi)/(yj-yi)+xi {
					inside = !inside
				}
				j = i
			}
			if inside {
				img.Set(x, y, c)
			}
		}
	}
}

func itoa(value int) string {
	if value == 64 {
		return "64"
	}
	return "256"
}
