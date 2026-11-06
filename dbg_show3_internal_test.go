package gopdf

import (
	"image/png"
	"os"
	"testing"
)

func TestDbgShow3(t *testing.T) {
	data, _ := os.ReadFile("showcase.pdf")
	img, err := RenderPageToImage(data, 0, RenderOption{DPI: 96})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dark: %d", countDarkPixels(img))
	f, _ := os.Create("test/out/dbg_show3.png")
	_ = png.Encode(f, img)
	f.Close()
}
