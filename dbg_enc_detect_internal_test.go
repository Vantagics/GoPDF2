package gopdf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDbgEncDetect(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	if err := pdf.SetEncryption(AESEncryptionConfig{
		Method:        EncryptRC4V2,
		UserPassword:  "pass",
		OwnerPassword: "ownerpass",
		Permissions:   PermissionsPrint,
	}); err != nil {
		t.Fatal(err)
	}
	pdf.AddPage()
	if err := pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf"); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 12); err != nil {
		t.Fatal(err)
	}
	pdf.SetXY(50, 50)
	_ = pdf.Cell(nil, "locked")
	path := filepath.Join(t.TempDir(), "locked.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	encNum := detectEncryption(data)
	t.Logf("detectEncryption: %d", encNum)
	dc, err := authenticate(data, "")
	t.Logf("authenticate(''): dc=%v err=%v", dc != nil, err)
	dc2, err2 := authenticate(data, "pass")
	t.Logf("authenticate('pass'): dc=%v err=%v", dc2 != nil, err2)
}
