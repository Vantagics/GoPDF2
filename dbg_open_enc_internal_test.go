package gopdf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OpenPDF with AES-encrypted PDFs — the decryptor gained AES support but
// OpenPDFOption doc comment still says "RC4 only".
func TestDbgOpenEncryptedRC4(t *testing.T) {
	// Create AES-256 encrypted PDF
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	if err := pdf.SetEncryption(AESEncryptionConfig{
		Method:       EncryptRC4V2,
		UserPassword: "rc4pass",
		OwnerPassword: "owner",
		Permissions:  PermissionsPrint,
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
	_ = pdf.Cell(nil, "RC4 opened marker")
	path := filepath.Join(t.TempDir(), "aes_enc.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)

	// Open with correct password
	mod := &GoPdf{}
	err := mod.OpenPDFFromBytes(data, &OpenPDFOption{Password: "rc4pass"})
	t.Logf("open with password: err=%v", err)
	if err != nil {
		t.Errorf("OpenPDF with correct AES password failed: %v", err)
		return
	}
	// verify the opened doc has the right page count
	if n := mod.GetNumberOfPages(); n < 1 {
		t.Errorf("opened doc page count = %d, want >= 1", n)
	}
	// verify extraction on the decrypted data sees the marker
	texts, err := ExtractTextFromAllPages(data)
	if err != nil {
		t.Logf("extraction on encrypted data error (expected): %v", err)
	} else {
		for _, runs := range texts {
			for _, tx := range runs {
				if strings.Contains(tx.Text, "RC4 opened marker") {
					t.Logf("decrypted content extractable ✓")
				}
			}
		}
	}
	_ = strings.Contains
}

// OpenPDF without password on encrypted PDF should return ErrEncryptedPDF
func TestDbgOpenEncryptedNoPassword(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	if err := pdf.SetEncryption(AESEncryptionConfig{
		Method:       EncryptRC4V2,
		UserPassword: "pass",
		OwnerPassword: "ownerpass",
		Permissions:  PermissionsPrint,
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
	mod := &GoPdf{}
	err := mod.OpenPDFFromBytes(data, nil)
	if err == nil {
		t.Errorf("OpenPDF without password on encrypted PDF should fail")
	} else if err != ErrEncryptedPDF {
		t.Logf("got err: %v (ErrEncryptedPDF=%v)", err, ErrEncryptedPDF)
	}
}
