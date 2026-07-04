package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestExtractBinaryFromTarGz_Found(t *testing.T) {
	// Build a tar.gz containing "mybin"
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	// Write a regular file named "mybin"
	if err := tw.WriteHeader(&tar.Header{
		Name:     "mybin",
		Mode:     0755,
		Size:     int64(len("ELF")),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("ELF")); err != nil {
		t.Fatal(err)
	}
	// Also write a directory entry to ensure it's skipped
	if err := tw.WriteHeader(&tar.Header{
		Name:     "subdir/",
		Mode:     0755,
		Size:     0,
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(tarBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := ExtractBinaryFromTarGz(&gzBuf, "mybin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "ELF" {
		t.Fatalf("expected ELF, got %s", string(data))
	}
}

func TestExtractBinaryFromTarGz_NestedPath(t *testing.T) {
	// Build a tar.gz containing "subdir/mybin"
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "subdir/mybin",
		Mode:     0755,
		Size:     int64(len("BIN")),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("BIN")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	gw.Write(tarBuf.Bytes())
	gw.Close()

	reader, err := ExtractBinaryFromTarGz(&gzBuf, "mybin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "BIN" {
		t.Fatalf("expected BIN, got %s", string(data))
	}
}

func TestExtractBinaryFromTarGz_NotFound(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	tw.WriteHeader(&tar.Header{Name: "other", Mode: 0755, Size: 0, Typeflag: tar.TypeReg})
	tw.Close()

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	gw.Write(tarBuf.Bytes())
	gw.Close()

	_, err := ExtractBinaryFromTarGz(&gzBuf, "mybin")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractBinaryFromTarGz_InvalidGzip(t *testing.T) {
	_, err := ExtractBinaryFromTarGz(strings.NewReader("not gzip"), "mybin")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractBinaryFromTarGz_ExeVariant(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	tw.WriteHeader(&tar.Header{Name: "mybin.exe", Mode: 0755, Size: int64(len("EXE")), Typeflag: tar.TypeReg})
	tw.Write([]byte("EXE"))
	tw.Close()

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	gw.Write(tarBuf.Bytes())
	gw.Close()

	reader, err := ExtractBinaryFromTarGz(&gzBuf, "mybin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "EXE" {
		t.Fatalf("expected EXE, got %s", string(data))
	}
}

func TestExtractBinaryFromZip_Found(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("mybin")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("ZIPBIN"))
	zw.Close()

	reader, err := ExtractBinaryFromZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "mybin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "ZIPBIN" {
		t.Fatalf("expected ZIPBIN, got %s", string(data))
	}
}

func TestExtractBinaryFromZip_NestedPath(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("subdir/mybin")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("NESTED"))
	zw.Close()

	reader, err := ExtractBinaryFromZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "mybin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "NESTED" {
		t.Fatalf("expected NESTED, got %s", string(data))
	}
}

func TestExtractBinaryFromZip_NonDirectorySkipped(t *testing.T) {
	// Ensure directories in zip are properly skipped
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Create a "directory" entry
	zw.Create("fake/")
	w, err := zw.Create("real.bin")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("REAL"))
	zw.Close()

	_, err = ExtractBinaryFromZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "real.bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractBinaryFromZip_NotFound(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.Create("other")
	zw.Close()

	_, err := ExtractBinaryFromZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "mybin")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractBinaryFromZip_InvalidZip(t *testing.T) {
	_, err := ExtractBinaryFromZip(strings.NewReader("not zip"), 8, "mybin")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractBinaryFromZip_ExeVariant(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("mybin.exe")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("WEXE"))
	zw.Close()

	reader, err := ExtractBinaryFromZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "mybin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := readAll(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "WEXE" {
		t.Fatalf("expected WEXE, got %s", string(data))
	}
}

func readAll(r interface {
	io.Reader
}) ([]byte, error) {
	b, err := io.ReadAll(r)
	return b, err
}