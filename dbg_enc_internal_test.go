package gopdf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end probe: RC4-protected write must produce an /Encrypt dictionary
// and must NOT leak plaintext into the raw file.
func TestEncryptedWriteNoPlaintextLeak(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{
		PageSize: *PageSizeA4,
		Protection: PDFProtectionConfig{
			UseProtection: true,
			Permissions:   PermissionsPrint | PermissionsCopy,
			UserPass:      []byte("user"),
			OwnerPass:     []byte("owner"),
		},
	})
	pdf.AddPage()
	if err := pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf"); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 12); err != nil {
		t.Fatal(err)
	}
	pdf.SetXY(50, 50)
	if err := pdf.Cell(nil, "Encrypted content here"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "enc.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "/Encrypt") {
		t.Errorf("no /Encrypt dictionary in output")
	}
	if strings.Contains(s, "Encrypted content") {
		t.Errorf("plaintext leaked into unencrypted stream")
	}
	t.Logf("encrypted write OK (%d bytes)", len(data))
}

// AES-256: the stronger cipher must also produce an /Encrypt dict without
// leaking plaintext, and the password path must recover the text.
func TestEncryptedWriteAES256NoPlaintextLeak(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	if err := pdf.SetEncryption(AESEncryptionConfig{
		Method:        EncryptAES256,
		UserPassword:  "u256",
		OwnerPassword: "o256",
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
	if err := pdf.Cell(nil, "AES256 secret marker QWERTY"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "aes256.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "/Encrypt") {
		t.Errorf("no /Encrypt dictionary in AES-256 output")
	}
	if strings.Contains(s, "AES256 secret marker") {
		t.Errorf("AES-256 plaintext leaked into raw file")
	}
	// password extraction must recover the marker
	texts, err := ExtractTextFromAllPagesWithPassword(data, "u256")
	if err != nil {
		t.Fatalf("AES-256 with-password extraction failed: %v", err)
	}
	found := false
	for _, runs := range texts {
		for _, tx := range runs {
			if strings.Contains(tx.Text, "QWERTY") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("AES-256 password extraction did not recover the marker")
	}
}

// AES-128 variant.
func TestEncryptedWriteAES128RoundTrip(t *testing.T) {
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	if err := pdf.SetEncryption(AESEncryptionConfig{
		Method:        EncryptAES128,
		UserPassword:  "u128",
		OwnerPassword: "o128",
		Permissions:   PermissionsPrint | PermissionsCopy,
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
	if err := pdf.Cell(nil, "AES128 marker 12321"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "aes128.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "AES128 marker") {
		t.Errorf("AES-128 plaintext leaked into raw file")
	}
	texts, err := ExtractTextFromAllPagesWithPassword(data, "u128")
	if err != nil {
		t.Fatalf("AES-128 with-password extraction failed: %v", err)
	}
	found := false
	for _, runs := range texts {
		for _, tx := range runs {
			if strings.Contains(tx.Text, "12321") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("AES-128 password extraction did not recover the marker")
	}
}
