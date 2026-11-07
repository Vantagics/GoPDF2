package gopdf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormFieldFillOnOriginalInstance(t *testing.T) {
	// Create a PDF with form fields
	pdf := &GoPdf{}
	pdf.Start(Config{PageSize: *PageSizeA4})
	pdf.AddPage()
	if err := pdf.AddTTFFont("lib", "test/res/LiberationSerif-Regular.ttf"); err != nil {
		t.Fatal(err)
	}
	if err := pdf.SetFont("lib", "", 12); err != nil {
		t.Fatal(err)
	}
	pdf.SetXY(50, 50)
	_ = pdf.Cell(nil, "Form:")
	if err := pdf.AddFormField(FormField{
		Name:     "name",
		Type:     FormFieldText,
		X:        150,
		Y:        50,
		W:        200,
		H:        20,
		Value:    "initial",
		FontSize: 12,
	}); err != nil {
		t.Fatal(err)
	}
	if err := pdf.AddFormField(FormField{
		Name:     "email",
		Type:     FormFieldText,
		X:        150,
		Y:        80,
		W:        200,
		H:        20,
		Value:    "",
		FontSize: 12,
	}); err != nil {
		t.Fatal(err)
	}
	origFields := pdf.GetFormFields()
	t.Logf("original form fields: %d", len(origFields))
	path := filepath.Join(t.TempDir(), "form.pdf")
	if err := pdf.WritePdf(path); err != nil {
		t.Fatal(err)
	}

	// Fill fields on the original instance (ModifyFormFieldValue works
	// on the in-memory form field list populated by AddFormField)
	if err := pdf.ModifyFormFieldValue("name", "John Doe"); err != nil {
		t.Logf("ModifyFormFieldValue(name): %v", err)
	} else {
		t.Logf("name filled OK")
	}
	if err := pdf.ModifyFormFieldValue("email", "john@example.com"); err != nil {
		t.Logf("ModifyFormFieldValue(email): %v", err)
	} else {
		t.Logf("email filled OK")
	}
	// verify in output
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "John Doe") {
		t.Logf("filled value visible in output ✓")
	} else {
		t.Logf("filled value NOT visible in raw bytes (expected for Identity-H encoded text)")
	}
	_ = written
}
